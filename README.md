# Falsos arquivos para honeypot HTTP

Contém 29 arquivos estáticos e `routes.json` para mapear rotas para conteúdos **inteiramente falsos**. Os valores com `DECOY` e os hosts `.invalid` não funcionam.

- Rotas exatas da allowlist são respondidas pelo honeypot; todas as outras são encaminhadas ao upstream configurado, incluindo `/` e `OPTIONS /`.
- Retorne `200 OK` nas rotas da lista com o MIME adequado (`text/plain`, `application/json` ou `text/html`).
- Rotas de traversal (`/../.env`, `/../../.env`) não são servidas; bloqueie-as antes de chegarem à aplicação. Não contorne os bloqueios de segurança para capturá-las.
- Faça o roteamento pela allowlist de `routes.json`, nunca por leitura dinâmica de caminhos requisitados.
- Use `Cache-Control: no-store`, não sirva segredos reais e isole da aplicação de produção.
- Para alertas de uso, gere credenciais-canário de um serviço próprio (ex.: Canarytokens AWS) e substitua somente o campo escolhido; não invente chaves que imitem identificadores reais.
- Não deixe `.env`, `.git`, `backup.sql` ou outros artefatos reais no diretório público.

### Exemplo de integração com Go

```go
// Use um handler dedicado ao invés de FileServer no diretório do projeto.
// Leia routes.json e carregue apenas os arquivos indicados ao iniciar.
// Em HTTP real, não mapeie o path de requisição diretamente a uma leitura de disco.
```

### Execução local

```sh
go run .
curl -i http://127.0.0.1:8089/.env
```

O servidor escuta somente em `127.0.0.1:8089`. Configure `HONEYPOT_UPSTREAM` com a origem HTTP(S) da aplicação (sem caminho base) para encaminhar as demais rotas; método, caminho, query e corpo são preservados. Sem upstream, essas rotas retornam `503 UPSTREAM_NOT_CONFIGURED`.

As métricas são persistidas em SQLite via `modernc.org/sqlite`; o arquivo padrão é `honeypot.sqlite`, alterável com `HONEYPOT_DB_PATH`. Goose aplica migrations SQL timestamped embutidas na inicialização. Os contadores são cumulativos; somente os 20 eventos mais recentes são mantidos. O arquivo e seus WAL/SHM são ignorados pelo Git. O banco usa WAL com `synchronous=NORMAL`: transações concluídas sobrevivem a encerramentos normais do processo, mas uma falha de energia pode perder commits recentes.

Defina `HONEYPOT_ADMIN_TOKEN` por um mecanismo seguro para habilitar o painel e a API administrativa. Abra `http://127.0.0.1:8089/dashboard/`; o token não é salvo pelo navegador. O painel mostra contadores e os últimos 20 eventos em memória, e permite trocar o upstream em tempo de execução. Essa alteração não persiste após reiniciar: o valor volta ao `HONEYPOT_UPSTREAM` inicial.

API administrativa (Bearer token obrigatório):

- `GET /v1/status` — uptime, contadores, estado do upstream e atividade recente.
- `GET /v1/config` — configuração atual e revisão (`ETag`).
- `PUT /v1/config` — atualiza `{ "upstream_url": "https://api.example.test" }`; envie o `ETag` recebido em `If-Match`. Edições com revisão antiga retornam `412 PRECONDITION_FAILED`.

O upstream dos testes é um servidor HTTP local temporário; os testes não dependem de API externa.

### Verificação e benchmark

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
scripts/benchmark.sh
```

O benchmark usa um upstream sintético em loopback, varredura de carga open-loop com duas repetições, teste isolado honeypot/proxy, spike, recuperação, soak de 60 s, amostras de CPU/RSS/tamanho SQLite e microbenchmark do gravador. Ele é limitado a loopback e a 5.000 req/s; cada execução guarda JSON bruto e resumo em `benchmark-results/` (ignorado pelo Git). O critério indicativo de capacidade é erro+drop ≤0,5% e p99 ≤250 ms, não um SLA de produção.
