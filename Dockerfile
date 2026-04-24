FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /out/lastfm-sheet-sync ./cmd/lastfm-sheet-sync

FROM alpine:3.20
WORKDIR /app
RUN adduser -D -g '' appuser
COPY --from=builder /out/lastfm-sheet-sync /usr/local/bin/lastfm-sheet-sync
COPY README.md ./README.md
USER appuser
ENTRYPOINT ["lastfm-sheet-sync"]
CMD ["sync"]
