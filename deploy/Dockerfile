# Multi-stage build: compile a static binary, ship it in a minimal image.
#   docker build -f deploy/Dockerfile -t gameserver .

FROM golang:1.27 AS build
WORKDIR /src
# Download modules first so this layer is cached between code changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/robot ./cmd/robot

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /out/robot /app/
COPY configs /app/configs
# 7000 TCP clients, 7001 WebSocket clients, 9100-9102 admin (metrics, health, pprof)
EXPOSE 7000 7001 9100 9101 9102
ENTRYPOINT ["/app/server"]
CMD ["-config", "/app/configs/allinone.yaml"]
