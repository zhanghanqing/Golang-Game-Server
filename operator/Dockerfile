# Operator image: a static binary in a distroless image.
#   docker build -t gameserver-operator:dev operator/

FROM golang:1.27 AS build
WORKDIR /src
# Download modules first so this layer is cached between code changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/operator ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/operator /operator
USER 65532:65532
ENTRYPOINT ["/operator"]
