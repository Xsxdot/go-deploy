# Stage 1: Build Vue frontend
FROM registry.cn-qingdao.aliyuncs.com/zilai/base:node-20-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 2: Build Go backend
FROM registry.cn-qingdao.aliyuncs.com/zilai/base:golang-1.26-alpine AS backend
WORKDIR /app
COPY backend/go.mod ./
RUN go mod download 2>/dev/null || true
COPY backend/ .
COPY --from=frontend /app/frontend/dist ./static
RUN CGO_ENABLED=0 go build -o demo-server .

# Stage 3: Runtime
FROM registry.cn-qingdao.aliyuncs.com/zilai/base:alpine-3.23
WORKDIR /app
COPY --from=backend /app/demo-server .
COPY --from=backend /app/static ./static
EXPOSE 8080
CMD ["./demo-server", "-static-dir", "./static", "-port", "8080"]
