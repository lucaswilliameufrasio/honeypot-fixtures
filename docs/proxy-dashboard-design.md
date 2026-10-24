# Proxy, API administrativa e painel

Status: decisão da implementação local — 2026-10-24

## Contexto

O projeto respondia somente às rotas honeypot e não encaminhava tráfego normal. A nova jornada deve deixar o honeypot interceptar apenas a allowlist, encaminhar o restante à aplicação existente e permitir que um operador local observe e ajuste o upstream.

## Decisões

- Rotas exatas em `routes.json` têm precedência sobre o proxy. Segmentos `.` e `..` são rejeitados antes do encaminhamento; caminhos desconhecidos são encaminhados sem alterar caminho, query, método ou corpo.
- O upstream é uma origem `http` ou `https`, configurável na inicialização via `HONEYPOT_UPSTREAM` ou em runtime pela API. A configuração runtime é mantida apenas em memória; reiniciar restaura o valor de ambiente.
- O painel `/dashboard/` é servido pela aplicação. `/v1/status` e `/v1/config` formam uma API versionada; leituras e alterações administrativas exigem Bearer token de `HONEYPOT_ADMIN_TOKEN`.
- `PUT /v1/config` exige `If-Match` com o ETag atual. A revisão opaca é trocada a cada alteração; uma escrita concorrente com revisão antiga falha com 412.
- Métricas são persistidas em SQLite por `modernc.org/sqlite`; Goose aplica migrations SQL timestamped embutidas no startup. Contadores são cumulativos e somente os 20 eventos mais recentes permanecem, evitando crescimento ilimitado de paths.
- SQLite usa WAL com `synchronous=NORMAL`; a política prioriza throughput local e consistência do banco, mas pode perder transações recentes em falha de energia. Reavaliar se as métricas virarem dado operacional crítico.
- O writer de métricas usa uma fila volátil limitada a 8.192 eventos, agrupa até 256 eventos já enfileirados sem janela de espera fixa, e confirma cada lote SQLite antes de `Record` retornar. `Close` fecha a admissão e drena a fila. Eventos em fila podem ser perdidos em encerramento abrupto; falha/timeout de persistência é logado sem alterar a resposta funcional.
- O painel guarda o token apenas em memória JavaScript da aba. CSP restringe scripts, estilos e chamadas à mesma origem; respostas de painel e API usam `no-store`.
- O servidor permanece vinculado a `127.0.0.1`. Para exposição atrás de proxy externo, esse proxy precisa proteger o painel/API e preservar os cabeçalhos de autorização.

## Alternativas e consequências

- Não usar upstream público hardcoded: o destino é específico do operador e um default externo faria requisições inesperadas. Testes usam `httptest` local.
- Não persistir a alteração runtime do upstream em arquivo ou banco: reduz complexidade e evita estado parcial, mas exige reaplicar alterações runtime após restart. Métricas têm armazenamento SQLite separado.
- Não incluir autenticação própria de usuários: a administração depende de um token de bootstrap fornecido pelo operador; integração com identidade corporativa fica fora do escopo.

## Limites conhecidos

- Configuração de upstream é uma capacidade privilegiada e permite ao operador apontar o proxy para qualquer origem HTTP(S); proteja o token e mantenha o processo isolado.
- O estado do painel é volátil e não substitui métricas/observabilidade externas.
