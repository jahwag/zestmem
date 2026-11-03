FROM docker.io/library/golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X github.com/jahwag/zestmem/internal/buildinfo.Version=${VERSION} -X github.com/jahwag/zestmem/internal/buildinfo.Revision=${REVISION} -X github.com/jahwag/zestmem/internal/buildinfo.Date=${CREATED}" -o /out/memoryd ./cmd/memoryd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="ZestMem" \
      org.opencontainers.image.description="Distributed, context-bounded memory for autonomous agent teams." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.source="https://github.com/jahwag/zestmem" \
      org.opencontainers.image.licenses="LicenseRef-Proprietary"
COPY --from=build /out/memoryd /usr/local/bin/memoryd
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=30 CMD ["/usr/local/bin/memoryd", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/memoryd"]
