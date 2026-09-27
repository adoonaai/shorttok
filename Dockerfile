FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /shorttok ./cmd/shorttok

FROM gcr.io/distroless/static:nonroot
COPY --from=build /shorttok /shorttok
EXPOSE 8080
ENTRYPOINT ["/shorttok"]
