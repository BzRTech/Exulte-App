# Deixa 2.1 (Exulte.exe) — código-fonte

Para compilar no Windows ou no Linux (Go 1.24+):

    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o Exulte.exe .

(Remova o bloco `replace` do go.mod para baixar as dependências normalmente.)

Arquivos: main.go (início, instância única), server.go (servidor local e API),
store.go (projeto e backups, compatível com a 2.0), convert.go (detecção de
formato e decodificadores AIFF/AU/WAV), mf_windows.go (conversão pelo Media
Foundation do Windows), ui_windows.go (janela WebView2), web/ (interface), sons/.
