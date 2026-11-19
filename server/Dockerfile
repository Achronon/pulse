# syntax=docker/dockerfile:1
FROM golang:1.26.11-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pulse .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /pulse /pulse
EXPOSE 8080
# bbolt state; mount a writable volume here (k8s: fsGroup 65532 for nonroot).
VOLUME ["/data"]
USER nonroot:nonroot
ENTRYPOINT ["/pulse"]
