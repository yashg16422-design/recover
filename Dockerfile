FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM alpine:3.20
WORKDIR /
COPY --from=build /out/server /usr/local/bin/server
COPY web /web
# App listens on $PORT if set (Render, HF Spaces), else :8090.
EXPOSE 8090
CMD ["server"]
