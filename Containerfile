FROM docker.io/library/golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/memoryd ./cmd/memoryd && \
    CGO_ENABLED=0 go build -trimpath -o /out/memoryctl ./cmd/memoryctl && \
    CGO_ENABLED=0 go build -trimpath -o /out/memory-mcp-bridge ./cmd/memory-mcp-bridge

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/memoryd /usr/local/bin/memoryd
COPY --from=build /out/memoryctl /usr/local/bin/memoryctl
COPY --from=build /out/memory-mcp-bridge /usr/local/bin/memory-mcp-bridge
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/memoryd"]
