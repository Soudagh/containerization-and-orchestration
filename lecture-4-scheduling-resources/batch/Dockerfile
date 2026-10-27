FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static:nonroot
ARG VERSION=dev
ENV APP_VERSION=$VERSION
COPY --from=build /app /app
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app"]
