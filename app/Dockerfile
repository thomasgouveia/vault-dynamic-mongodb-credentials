FROM public.ecr.aws/docker/library/busybox:stable-uclibc as busybox

FROM public.ecr.aws/docker/library/golang:1.20-alpine3.18 AS builder
# RUN addgroup -S nonroot && adduser -S -u 10000 -g nonroot nonroot
WORKDIR /go/src/app
COPY . .
RUN CGO_ENABLED=0 go install -ldflags='-extldflags "-static"' -tags timetzdata

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=busybox /bin/sh /bin/sh
COPY --from=builder /go/bin/demo /demo-app
ENTRYPOINT ["/demo-app"]
