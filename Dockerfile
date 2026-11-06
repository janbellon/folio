FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/folio .
RUN mkdir /out/tmp && chmod 1777 /out/tmp

FROM scratch
COPY --from=build /out/folio /folio
COPY --from=build /out/tmp /tmp
USER 65534:65534
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/folio", "healthcheck"]
ENTRYPOINT ["/folio"]
