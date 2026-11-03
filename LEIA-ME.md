# Deixa — mesa de áudio para espetáculos (Windows)

Programa leve para operar o som de um espetáculo: sequência de músicas, pads de
efeitos sonoros e deixas programadas (efeito ou pausa em um tempo da música).

**`Deixa.exe` tem cerca de 8 MB, é um arquivo só e não precisa de instalação.**
Ele usa o Microsoft Edge WebView2, que já vem no Windows 10 e 11.

## Como usar

1. Coloque o `Deixa.exe` numa pasta só dele (por exemplo `Documentos\Deixa`) e
   dê dois cliques. Na primeira vez o Windows pode mostrar "O Windows protegeu
   o computador": clique em **Mais informações → Executar assim mesmo**
   (acontece com todo programa sem assinatura digital paga).
2. **＋ Músicas** (ou arraste os arquivos para a sequência) importa MP3, WAV,
   OGG, M4A, FLAC… Os arquivos são **copiados para dentro do Deixa**, na pasta
   `Deixa-dados` criada ao lado do `.exe`.
3. Os efeitos **Campainha**, **Campainha elétrica**, **Freio de carro** e
   **Freada longa** já vêm prontos. **＋ Efeitos** (ou arrastar para o painel
   de efeitos) adiciona outros. Duplo clique no nome renomeia.
4. Selecione uma música e use **＋ Deixa** para disparar um efeito ou pausar a
   música em um tempo (mm:ss).

| Tecla | Ação |
|---|---|
| Espaço | Tocar / pausar a música |
| Esc | Parar todos os sons |
| 1 a 9 | Disparar (ou parar) o efeito do pad |

Tudo é salvo automaticamente. Enquanto o Deixa estiver aberto o computador não
entra em suspensão nem apaga a tela.

### Levar para outro computador

Copie o `Deixa.exe` **junto com a pasta `Deixa-dados`**, ou use
**Salvar backup** (gera um `.zip` com o projeto e todos os áudios na pasta
Downloads) e depois **Abrir backup** no outro computador. Backups `.json`
exportados pela versão de navegador do Deixa também podem ser abertos: as
músicas e efeitos deles são importados (as demonstrações são descartadas).

Se a pasta do `.exe` não permitir gravação (ex.: `Arquivos de Programas`), os
dados ficam em `%APPDATA%\Deixa`. O botão **Pasta de dados** abre a pasta em uso.

## Por que não trava

- O projeto e os áudios ficam em arquivos no disco, não no armazenamento do
  navegador; importar uma música de 115 MB leva menos de 1 s, sem congelar a tela.
- A música toca em streaming de um servidor local (só `127.0.0.1`), então o
  arquivo não é carregado inteiro na memória e a busca é instantânea.
- Os efeitos ficam pré-carregados na memória (Web Audio): disparam na hora e
  vários podem tocar juntos sem interromper a música.
- As deixas são verificadas a cada 30 ms, e o Windows não desacelera o
  programa quando a janela está minimizada ou em segundo plano.
- Backup é um `.zip` gerado em streaming (sem converter áudios para texto).

## Compilar

Requer Go 1.22+ (em Linux, macOS ou Windows):

```sh
./build.sh            # gera dist/Deixa.exe
```

Para desenvolver em Linux/macOS, `go run .` abre a mesma interface no navegador.
O GitHub Actions (`.github/workflows/build-windows.yml`) gera o `.exe` a cada push.

Estrutura:

- `main.go`, `server.go`, `store.go` — servidor local, projeto e áudios em disco, backup.
- `ui_windows.go` — janela WebView2, instância única, impede suspensão.
- `web/` — interface (HTML/CSS/JS, fontes embutidas para funcionar offline).
- `sons/` — efeitos padrão, gerados por `tools/gerar_sons.py` (síntese própria,
  livres para distribuir).
- `winres/` — ícone e manifesto do executável.
