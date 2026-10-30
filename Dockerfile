# build: CGO_ENABLED=0 keeps the binary static, so the runtime image is tiny.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/playpile ./cmd/playpile

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/playpile /playpile
VOLUME ["/data"]
ENV PLAYPILE_DATA_DIR=/data PLAYPILE_ADDR=:8080
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/playpile"]
