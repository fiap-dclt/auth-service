# ==========================================
# Stage 1: Build
# ==========================================
FROM golang:1.21-alpine AS builder

# Instalar dependências essenciais de sistema
# ca-certificates: Para fazer requisições HTTPS externas (como autenticações em outros serviços)
# tzdata: Para manipulação correta de fusos horários
RUN apk add --no-cache git ca-certificates tzdata && update-ca-certificates

# Criar um usuário não-root no builder
# Usaremos o UID 65532 (padrão em contêineres restritos) para gerar o arquivo /etc/passwd
ENV USER=nonroot
ENV UID=65532
RUN adduser -D -g "" -H -s "/sbin/nologin" -u "${UID}" "${USER}"

WORKDIR /app

# Copiar go.mod e go.sum
COPY go.mod go.sum ./
RUN go mod download

# Copiar o restante do código fonte
COPY handlers.go .
COPY key.go .
COPY main.go .

# Compilar o binário
# -ldflags="-s -w" remove símbolos de debug, reduzindo muito o tamanho final do binário
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -a -installsuffix cgo -o auth-service .

# ==========================================
# Stage 2: Imagem Final (Scratch)
# ==========================================
FROM scratch

# Copiar timezone e certificados de segurança do builder
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copiar as identidades do usuário não-root
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# Copiar APENAS o binário compilado
COPY --from=builder /app/auth-service /auth-service

# Definir a porta (metadado)
EXPOSE 8001

# Assumir a identidade do usuário não-root
USER nonroot:nonroot

# Executar o binário diretamente
ENTRYPOINT ["/auth-service"]