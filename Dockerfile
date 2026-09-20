FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY server ./server
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hex-server ./cmd/hex-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hex-server /hex-server
ENV HEX_ADDR=127.0.0.1:8081
ENTRYPOINT ["/hex-server"]
