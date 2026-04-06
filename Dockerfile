FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /famclaw ./cmd/famclaw/

FROM scratch
COPY --from=builder /famclaw /famclaw
COPY --from=builder /src/policies /policies
EXPOSE 8080
ENTRYPOINT ["/famclaw"]
