# Build a static single binary, run on a minimal distroless base.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO disabled → pure-Go static binary (modernc sqlite, aws-sdk, prometheus all pure-Go).
# VERSION stamps `mayu version` and the control plane's fleet version view
# (roadmap ③ phase 1); unset builds report "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/mayu ./cmd/mayu

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mayu /usr/local/bin/mayu
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/mayu"]
CMD ["serve", "--config", "/etc/inferplane/config.json"]
