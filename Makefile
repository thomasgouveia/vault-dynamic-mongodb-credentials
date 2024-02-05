APP_VERSION ?= 1.0.0

build/docker:
	docker buildx build \
      --tag thomasgouveia/vault-dynamic-credentials:$(APP_VERSION) \
      --platform linux/arm64 \
      --push ./app

deploy/k3d:
	k3d cluster create vault-dynamic-credentials \
		--agents 2