# tunnel-broker image. Build context: tools/ptun. Multi-arch via buildx (TARGETARCH).
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/tunnel-broker ./cmd/tunnel-broker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tunnel-broker /tunnel-broker
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/tunnel-broker"]
