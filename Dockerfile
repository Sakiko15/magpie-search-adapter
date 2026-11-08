FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/magpie-search-adapter .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/magpie-search-adapter /magpie-search-adapter
USER nonroot:nonroot
EXPOSE 8787
ENTRYPOINT ["/magpie-search-adapter"]