# Dynamic Database Credentials - HashiCorp Vault

This repository contains the demo of my talk about [Dynamic Database Credentials with HashiCorp Vault](https://thomasgouveia.github.io/talks/dynamic-database-credentials-with-hashicorp-vault). It illustrates the Vault database secret engine with MongoDB and the Vault agent.

## Prerequisites

- A Kubernetes cluster
- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- [Helm](https://helm.sh/docs/intro/install/)

## Step-by-step

### Install components

Add the two following Helm repositories:

```bash
$ helm repo add hashicorp https://helm.releases.hashicorp.com
$ helm repo add mongodb https://mongodb.github.io/helm-charts
```

Install Vault:

```bash
$ helm upgrade vault hashicorp/vault \
    --set server.dev.enabled=true \
    --install
```

> **Note**: For the sake of simplicity, Vault is deployed in development mode. **Do not deploy this in production.**

Install the [MongoDB community operator](https://github.com/mongodb/mongodb-kubernetes-operator/tree/master). We will use it to deploy a MongoDB cluster:

```bash
$ helm upgrade mongodb-operator mongodb/community-operator \
    --set operator.watchNamespace=default \
    --set database.namespace=default \
    --namespace mongodb-system \
    --create-namespace \
    --install
```

Now, create a secret to store the MongoDB root user password. Here, we will use the password `root`:

```bash
$ kubectl create secret generic mongodb-root-password --from-literal=password=root
```

Execute the following command to deploy a 3 nodes MongoDB cluster:

```bash
$ kubectl apply -f ./k8s/manifests/mongodb.yml
```

Wait for the MongoDB pods to be up and running:

```bash
$ kubectl get pods -l app=mongodb-svc

# Expected output
NAME        READY   STATUS    RESTARTS   AGE
mongodb-1   2/2     Running   0          110s
mongodb-2   2/2     Running   0          56s
mongodb-0   2/2     Running   0          2m44s
```

### Configure Vault

#### MongoDB Database Engine

We need to configure a database secret engine into our Vault in order to allow credentials issuance. To do so, follow the steps below. 

First of all, we need to create a user into the MongoDB cluster that will be used by Vault to generate/revoke the credentials. To do so, open a remote shell into one of your MongoDB pods, log in with the `root` user and create a `vault-manager` user with a password set to `initial`, as showed in the following commands:

```bash
$ kubectl exec -it mongodb-0 -c mongod -- mongosh -u root -p root
```

From within the pod, create the `vault-manager` user:

```bash
use admin
db.createUser(
  {
    user: "vault-manager",
    pwd: "initial",
    roles: [
       { role: "userAdminAnyDatabase", db: "admin" }
    ]
  }
)
```

Exit your command, and try to log in as the `vault-manager` with the password `initial`:

```bash
$ kubectl exec -it mongodb-0 -c mongod -- mongosh -u vault-manager -p initial
```

We'll trigger a root credential rotation (e.g. rotating the `initial` password) just after the successful configuration of our MongoDB database engine in Vault. **After the rotation, only Vault will know the password of your `vault-manager` user**. This is why you must always use a dedicated user for Vault.

Now that our MongoDB server is ready, we can configure the Vault MongoDB engine. Open a remote shell into your Vault pod:

```bash
$ kubectl exec -it vault-0 -- sh
```

**From now, all the commands below are executed from within the pod.**

Enable a new database engine at the path `mongodb`:

```bash
$ vault secrets enable -path=mongodb database

Success! Enabled the database secrets engine at: mongodb/
```

Now, configure a new connection into the previously created engine:

```bash
$ vault write mongodb/config/mongodb \
    plugin_name=mongodb-database-plugin \
    connection_url="mongodb://{{username}}:{{password}}@mongodb-0.mongodb-svc:27017,mongodb-1.mongodb-svc:27017,mongodb-2.mongodb-svc:27017/?replicaSet=mongodb" \
    username_template="vault-{{ .RoleName }}-{{ random 8 }}" \
    username="vault-manager" \
    password="initial"
 
Success! Data written to: mongodb/config/mongodb
```

At this point, Vault is now able to communicate with MongoDB to generate/revoke credentials. As said previously, we want to rotate the `initial` password to improve the security of our set-up:

```bash
$ vault write -force mongodb/rotate-root/mongodb

Success! Data written to: mongodb/rotate-root/mongodb
```

If you now try to connect as the `vault-manager` user with the `initial` password:

```bash
$ kubectl exec -it mongodb-0 -c mongod -- mongosh -u vault-manager -p initial
```

You should have the following error:

```text
MongoServerError: Authentication failed.
command terminated with exit code 1
```

#### Kubernetes Authentication

To allow our Kubernetes applications to authenticate with Vault, we need to configure the proper authentication method.

Open a remote shell into your Vault pod:

```bash
$ kubectl exec -it vault-0 -- sh
```

**From now, all the commands below are executed from within the pod.**

Enable the Kubernetes authentication method:

```bash
$ vault auth enable kubernetes
```

Configure the method with the endpoint of your Kubernetes cluster. As we are in the same cluster here, we can use the following command:

```bash
$ vault write auth/kubernetes/config \
    kubernetes_host=https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT
```

Your Vault is ready to authenticate your Kubernetes applications.

#### Roles and policies

Now that the Kubernetes authentication method and the MongoDB database engine are ready, we need to create a policy and two roles to allow our application to:

1. Authenticate with Vault
2. Generate a credentials to access MongoDB

Our application for this demo will be called `demo-app`. Open a remote shell into your Vault pod:

```bash
$ kubectl exec -it vault-0 -- sh
```

**From now, all the commands below are executed from within the pod.**

First, we create the role into the MongoDB database engine to allow our application to generate its own credentials:

```bash
$ vault write mongodb/roles/demo-app \
    db_name=mongodb \
    creation_statements="{ \"db\": \"demo-app\", \"roles\": [{ \"role\": \"readWrite\" }] }" \
    revocation_statements="{\"db\": \"demo-app\"}" \
    default_ttl="2m" \
    max_ttl="10m"
    
Success! Data written to: mongodb/roles/demo-app 
```

The `default_ttl` and `max_ttl` attributes are respectively set to a low duration for the purpose of the demo:

- `default_ttl`: The default time-to-live of the lease. 
- `max_ttl`: Maximum time-to-live of the lease.  

Vault will try to renew the lease based on the `max_ttl`. For instance here, a lease with TTL of 2 minutes can be theoretically smoothly renewed maximum 5 times (2m + 2m + 2m + 2m + 2m <= 10m) after which it will be revoked and a new lease will be required.

**But what happens if we try to renew the lease after the `max_ttl`, 10m?** (_spoiler: nothing good_):

Generally, client applications will try to renew the lease some time before the expiration (e.g. for a lease of 2m, try to renew it 30s before expiration). If we apply this scenario here:

- t1 (0m): acquire lease
- t2 (1m30): renew
- t3 (3m): renew 
- t4 (4m30): renew
- t5 (6m): renew
- t6 (7m30): renew
- t7 (9m): renew
- **t8 (10m30): renew failed**

At **t8**, the lease will fail to renew as the lease exceed the `max_ttl`, set to `10m`. If your application does not handle it gracefully, this behavior could lead to a downtime, because for 30s, you'll have no valid MongoDB credentials (from 10m to 10m30). [We'll see a solution to handle this at the end of the demo](#handle-lease-expiration-gracefully).

Now, add the role to the `allowed_roles` list of the previously configured MongoDB connection:

```bash
$ vault write mongodb/config/mongodb \
    allowed_roles="demo-app"
    
Success! Data written to: mongodb/config/mongodb
```

At this point, you should now be able to generate a credential for this role:

```bash
$ vault read mongodb/creds/demo-app

Key                Value
---                -----
lease_id           mongodb/creds/demo-app/Vtn0QKjYFvYQNQkNNaqraKOo
lease_duration     2m
lease_renewable    true
password           O3sURYJ-1oyVYuREBCJW
username           vault-demo-app-8kfyZAcz
```

Now, we want to allow our Kubernetes application to execute the above call automatically, without manual intervention. To do so, we need to create a policy to restrict the actions allowed for our application:

```bash
$ vault policy write demo-app - <<EOF
path "mongodb/creds/demo-app" {
  capabilities = [ "read", "update"]
}
EOF

Success! Uploaded policy: demo-app
```

In the previous above, we allow read and update operations on the path `mongodb/creds/demo-app`. Any other paths/actions not listed in the policy are by default forbidden by Vault.

Finally, we need to authorize our application to authenticate with Vault through the Kubernetes authentication. In the role below, we restrict the application service account name to `demo-app` and the namespace to `default`. Note that this role will inherit the policy `demo-app`:

```bash
$ vault write auth/kubernetes/role/demo-app \
    bound_service_account_names=demo-app \
    bound_service_account_namespaces=default \
    policies=demo-app \
    ttl=1h \
    max_ttl=8d
    
Success! Data written to: auth/kubernetes/role/demo-app
```

All our infrastructure components are now correctly configured. The only missing thing is the deployment of our application into our cluster.

### Deploy the application

To deploy our application, we'll use a simple `Deployment` and a `ServiceAccount`. The service account name must match the name you defined previously in the Kubernetes backend role, at the key `bound_service_account_names`.

Here is the YAML manifest:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
  labels:
    app: demo-app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo-app
  template:
    metadata:
      name: demo-app
      labels:
        app: demo-app
      annotations:
        vault.hashicorp.com/role: 'demo-app'
        vault.hashicorp.com/agent-inject: 'true'
        vault.hashicorp.com/agent-cache-enable: "true"
        vault.hashicorp.com/agent-inject-template-mongodb: |
          {{- with secret "mongodb/creds/demo-app" -}}
            export MONGODB_URI="mongodb://{{ .Data.username }}:{{ .Data.password }}@mongodb-0.mongodb-svc:27017,mongodb-1.mongodb-svc:27017,mongodb-2.mongodb-svc:27017/?replicaSet=mongodb&authSource=${MONGODB_DATABASE}"
          {{- end }}
    spec:
      serviceAccountName: demo-app
      containers:
        - name: demo-app
          imagePullPolicy: Always
          image: thomasgouveia/vault-dynamic-credentials:1.0.0
          env:
            - name: MONGODB_DATABASE
              value: demo-app
          # Override the default container entrypoint to source the environment variables 
          # generated by Vault (see annotation: vault.hashicorp.com/agent-inject-template-mongodb) before launching the application.
          command: ["sh", "-c"]
          args: [". /vault/secrets/mongodb && /demo-app"]
          ports:
            - containerPort: 8080
          livenessProbe:
            failureThreshold: 1
            httpGet:
              path: /_/health/liveness
              port: 8080
          readinessProbe:
            httpGet:
              path: /_/health/readiness
              port: 8080
```

Pay attention to the pod annotations, at path `spec.template.metadata.annotations`. These annotations enable the Vault sidecar on our pod. Below are explained the most important ones:

- `vault.hashicorp.com/agent-inject`: Enable injection of the Vault sidecar for this pod.


- `vault.hashicorp.com/role`: Configures the Vault role used by the Vault Agent auto-auth method.

- `vault.hashicorp.com/agent-inject-template-mongodb`: Configures the template Vault Agent should use for rendering a secret.

> **Note**: In the last annotation above, the suffix `mongodb` is an arbitrary name that will be used to render the file in `/vault/secrets/`.

You can find the reference of each annotation on the [official Vault documentation](https://developer.hashicorp.com/vault/docs/platform/k8s/injector/annotations).

Deploy it with the following command:

```bash
$ kubectl apply -f ./k8s/manifests/app
```

After few seconds, if you list the pods with the label `app=demo-app`, you should see your application up and running:

```bash
$ kubectl get pods -l app=demo-app

NAME                       READY   STATUS    RESTARTS   AGE
demo-app-ff7558f44-dh45m   2/2     Running   0          69s
```

In a new terminal, open a port forward (`8080`) to the pod:

```bash
$ kubectl port-forward demo-app-ff7558f44-dh45m 8080:8080
```

In your main terminal, execute the following curl call. It should return to you the credentials generated for this application:

```bash
$ curl http://localhost:8080

{
  "password": "6bvooNmohw8hd-qzpiSI",
  "username": "vault-demo-app-n1QzUuH0"
}
```

Let's now scale our deployment to 2 replicas:

```bash
$ kubectl scale --replicas 2 deployment/demo-app
```

Check that you have 2 pods running:

```bash
$ kubectl get pods -l app=demo-app

NAME                       READY   STATUS    RESTARTS    AGE
demo-app-ff7558f44-dh45m   2/2     Running   0           2m44s
demo-app-ff7558f44-v427g   2/2     Running   0           41s
```

In another terminal, open a port-forward to the second pod:

```bash
$ kubectl port-forward demo-app-ff7558f44-dh45m 8081:8080
```

And curl the endpoint:

```bash
$ curl http://localhost:8081

{
  "password": "-ZbO--Yh8zwXPnVue0yL",
  "username": "vault-demo-app-5fE8DGgE"
}
```

As you can see, each pod has its own set of credentials.

### Handle lease expiration gracefully

As explained in [this section](#roles-and-policies), a lease will expire after the cumulative lease TTL exceed the `max_ttl` of it. To avoid any service disruption, it is needed to handle this gracefully so that our applications continue to work during the renewal process.

A solution highlighted in the blog [Refresh Secrets for Kubernetes Applications with Vault Agent](https://www.hashicorp.com/blog/refresh-secrets-for-kubernetes-applications-with-vault-agent) leverage system signals to terminate the process when Vault re-render the secret template (a.k.a. lease renewal). This solution works great with lease renewal, but Vault agent is not able actually to handle the case when cumulative TTL exceed the `max_ttl` of the lease. If we re-use our example with `t1...t8`, at `t8`, **the pod will crash totally because Vault Agent will not be able to renew the lease anymore**. The pod will be rescheduled by Kubernetes, but it can introduce some downtime.

A solution I prefer to overcome this issue is to simply trigger a deployment rollout some time before the `max_ttl`. If we consider our lease with a `ttl=2m` and a `max_ttl=10m`, we theoretically can renew the lease 6 times (if we renew the lease 30s after the TTL). The idea here is to trigger a rollout of the Kubernetes deployment at `~max_ttl/2`, so in our example, `5m`. 

**But why?**

- First, rolling out the pods will trigger a graceful shutdown of your application, and when the new pod gets scheduled, it will acquire a new lease, different from the previous one. 
- Second, Vault will automatically revoke the unused leases when they expire. 

Let's illustrate that solution:

- t1 (0m): acquire lease
- t2 (1m30): renew
- t3 (3m): renew
- t4 (4m30): renew
- **t5 (5m): rollout is triggered, and pods acquire new leases (fresh MongoDB credentials)**
- t6 (6m30): previous leases are totally revoked

> **Note**: You can automatically revoke the leases on shutdown using the `vault.hashicorp.com/agent-revoke-on-shutdown: "true"` annotation.

The **delta** time (the time between the rollout and the `max_ttl`) is arbitrary here and can be adjusted depending on your application start up times. The idea is to have all the pods of your application up and running before the credentials of an application expires.

This solution assume that your applications adheres to the [disposability](https://12factor.net/fr/disposability) principle of the 12 Factors applications.

You can find an example of what I call a "reloader" in this [folder](./k8s/manifests/reloader). You can deploy it with the following command:

```bash
$ kubectl delete -f ./k8s/manifests/app && \
  kubectl apply -f ./k8s/manifests/reloader && \
  kubectl apply -f ./k8s/manifests/app
```

> **Note**: In production, consider adding a reloader configuration to your Helm charts for example to ensure that everything is deployed at the same time.

### Cleaning Up

To clean up everything from this demo:

```bash
$ kubectl delete -f ./k8s/manifests/app --ignore-not-found=true && \
  kubectl delete -f ./k8s/manifests/reloader --ignore-not-found=true  && \
  kubectl delete -f ./k8s/manifests/mongodb.yml --ignore-not-found=true  && \
  helm uninstall mongodb-operator -n mongodb-system && \
  helm uninstall vault && \
  kubectl delete \
    secret/mongodb-root-password \
    pvc/data-volume-mongodb-0 \
    pvc/data-volume-mongodb-1 \
    pvc/data-volume-mongodb-2 \
    pvc/logs-volume-mongodb-0 \
    pvc/logs-volume-mongodb-1 \
    pvc/logs-volume-mongodb-2
```
