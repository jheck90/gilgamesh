FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gmesh ./cmd/gmesh

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata tesseract-ocr tesseract-ocr-data-eng \
 && adduser -D -u 1000 gmesh \
 && mkdir /data && chown gmesh:gmesh /data
COPY --from=build /out/gmesh /usr/local/bin/gmesh
USER gmesh
ENV GMESH_DB=/data/gilgamesh.db GMESH_ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["gmesh"]
CMD ["serve"]
