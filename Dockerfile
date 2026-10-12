FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gmesh ./cmd/gmesh

FROM alpine:3.22 AS base
RUN apk add --no-cache ca-certificates tzdata tesseract-ocr tesseract-ocr-data-eng \
 && adduser -D -u 1000 gmesh \
 && mkdir /data && chown gmesh:gmesh /data
COPY --from=build /out/gmesh /usr/local/bin/gmesh
ENV GMESH_DB=/data/gilgamesh.db GMESH_ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["gmesh"]
CMD ["serve"]

# The :claude-code image adds the `claude` CLI, so GMESH_PROVIDER=claude-code
# can pay for adding books with a Claude subscription. Build it with
# `--target claude-code`.
FROM base AS claude-code
ARG CLAUDE_CODE_VERSION=2.1.296
RUN apk add --no-cache bash curl libgcc libstdc++ ripgrep
USER gmesh
RUN curl -fsSL https://claude.ai/install.sh | bash -s "$CLAUDE_CODE_VERSION"
ENV PATH=/home/gmesh/.local/bin:$PATH USE_BUILTIN_RIPGREP=0 DISABLE_AUTOUPDATER=1

FROM base
USER gmesh
