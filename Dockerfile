FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/reconciler ./cmd/reconciler && CGO_ENABLED=0 go build -trimpath -o /out/demo ./cmd/demo && CGO_ENABLED=0 go build -trimpath -o /out/import-indexer ./cmd/import-indexer

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 reconciler
WORKDIR /app
COPY --from=build /out/ /app/
USER reconciler
EXPOSE 8081
ENTRYPOINT ["/app/reconciler"]
