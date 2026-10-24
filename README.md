# Falsos arquivos para honeypot HTTP

Contém 29 arquivos estáticos e `routes.json` para mapear rotas para conteúdos **inteiramente falsos**. Os valores com `DECOY` e os hosts `.invalid` não funcionam.

- Rota `/` e método `OPTIONS /`: **não** sobrescreva seu site normal.
- Retorne `200 OK` nas rotas da lista com o MIME adequado (`text/plain`, `application/json` ou `text/html`).
- Rotas de traversal (`/../.env`, `/../../.env`) podem ser normalizadas/bloqueadas antes de chegar à aplicação. Não contorne os bloqueios de segurança para capturá-las.
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
go run main.go
curl -i http://127.0.0.1:8089/.env
```

O servidor de exemplo escuta só em `127.0.0.1:8089`; coloque-o atrás de um proxy ou adapte o handler ao seu roteador existente.
