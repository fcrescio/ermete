# ermete

Ermete è un server Go **single-session** per Android che combina signaling WebRTC (WS), audio bidirezionale, DataChannel comandi e upload immagini periodico.

## Caratteristiche principali

- `GET /v1/ws`: signaling WebRTC con `offer/answer/candidate/bye` JSON.
- Audio WebRTC:
  - ingresso dal client su track Opus;
  - uscita server->client su track Opus locale;
  - demo pipeline con **loopback RTP** (i pacchetti audio ricevuti vengono inoltrati in uscita).
- DataChannel `cmd`:
  - envelope JSON `{type,text,bin}`;
  - `ping`/`pong`, `server_status`, `say`.
- `POST /v1/frames` per JPEG/PNG raw o multipart con dedup/idempotenza.
- Politica sessione configurabile:
  - `reject_second` (default): secondo client rifiutato;
  - `kick_previous`: il nuovo client sostituisce il precedente.
- Observability: `/healthz`, `/readyz`, `/metrics` (Prometheus), logging strutturato con request id.
- Robustezza: timeout HTTP, limiti upload, rate limit base per IP, graceful shutdown.

## Configurazione via environment

| Variabile | Default | Descrizione |
|---|---|---|
| `HTTP_ADDR` | `:8080` | bind HTTP |
| `DATA_DIR` | `/data` | base dir persistenza |
| `MAX_UPLOAD_MB` | `10` | limite upload frame |
| `CORS_ALLOWED_ORIGINS` | *(vuoto)* | CSV origini abilitate CORS |
| `SESSION_POLICY` | `reject_second` | `reject_second` o `kick_previous` |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | vuoto | se presenti abilita HTTPS nativo |
| `WEBRTC_STUN_URLS` | vuoto | CSV STUN URLs |
| `WEBRTC_TURN_URLS` | vuoto | CSV TURN URLs |
| `WEBRTC_TURN_USER` / `WEBRTC_TURN_PASS` | vuoto | credenziali TURN |

> Deploy consigliato: dietro reverse proxy (Nginx/Traefik) per TLS termination e hardening edge.

## Esecuzione locale

```bash
go mod tidy
go run ./cmd/ermete
```

Check rapidi:

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -i http://localhost:8080/metrics
```

## Docker

```bash
docker build -t ermete .
docker run --rm -p 8080:8080 -v "$(pwd)/data:/data" ermete
```

## Signaling WebSocket

Endpoint: `GET /v1/ws`

Messaggi supportati:

```json
{"type":"offer","sdp":"..."}
{"type":"answer","sdp":"..."}
{"type":"candidate","candidate":{"candidate":"...","sdpMid":"0","sdpMLineIndex":0}}
{"type":"error","message":"..."}
{"type":"bye"}
```

### Trickle ICE

- Client invia `candidate` appena disponibile.
- Server invia `candidate` via WS da callback `OnICECandidate`.

### Recovery connessione

- Gli stati ICE/PeerConnection aggiornano `SessionManager`.
- In caso di fail/disconnect lato peer, la sessione viene chiusa e liberata.
- Il client può rifare handshake WS+offer (o ICE restart con nuova offer).

## DataChannel `cmd`

Envelope JSON:

```json
{"type":"ping","text":"...","bin":"<base64 opzionale>"}
```

Comandi:

- `ping` -> `pong`
- `server_status` -> ritorna stato sessione, ultimo frame, count frames, uptime
- `say` -> placeholder: conferma modalità loopback audio attiva

Se arriva payload binario non-string, il server risponde con `pong` e `bin` base64.

## Upload frame

Endpoint: `POST /v1/frames`

Header opzionali:

- `X-Frame-Id`
- `X-Timestamp`
- `X-Idempotency-Key`

Accetta:

- raw body (`Content-Type: image/jpeg|image/png`)
- multipart/form-data (`file`)

Esempio raw:

```bash
curl -X POST http://localhost:8080/v1/frames \
  -H "Content-Type: image/jpeg" \
  -H "X-Frame-Id: frame-123" \
  -H "X-Timestamp: 2026-01-01T10:00:00Z" \
  -H "X-Idempotency-Key: abc-123" \
  --data-binary @sample.jpg
```

Risposta:

```json
{
  "status": "ok",
  "duplicate": false,
  "frame": {
    "frame_id": "frame-123",
    "timestamp": "2026-01-01T10:00:00Z",
    "file_name": "frame-123_...jpg",
    "path": "/data/frames/...",
    "size": 12345,
    "content_type": "image/jpeg",
    "sha256": "...",
    "received_at": "..."
  },
  "request_id": "..."
}
```

## Note TURN/NAT

- Impostare almeno uno STUN pubblico in `WEBRTC_STUN_URLS`.
- Per reti mobili/NAT simmetrici usare TURN (`WEBRTC_TURN_URLS`, user, pass).
- Pion usa ICE standard; con TURN sono supportati relay UDP/TCP in base al server TURN.

## Testing

```bash
go test ./...
```

Copertura minima inclusa:

- parsing env config
- upload handler (limite dimensione + naming sicuro)
- policy sessione `reject/kick`
