# 1. web app
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# the Magic export script is downloadable from the "Import from Magic" page
COPY tools/magic-export/Export-Magic.ps1 ./public/tools/Export-Magic.ps1
RUN npm run build

# 2. Go server
FROM golang:1.27-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/paxve ./cmd/paxve

# 3. small runtime image, runs as non-root
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=api /out/paxve /app/paxve
COPY --from=web /web/dist /app/web
ENV WEB_DIR=/app/web ADDR=:8080 TZ=Asia/Beirut
EXPOSE 8080
ENTRYPOINT ["/app/paxve"]
CMD ["serve"]
