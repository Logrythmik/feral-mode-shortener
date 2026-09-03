# Build for linux/amd64 (Cloud Run): docker build --platform linux/amd64 -t feral-shortener .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /shortener .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /shortener /shortener
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/shortener"]
