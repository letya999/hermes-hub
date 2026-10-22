package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenericCredentialProbeUsesManagedNetwork(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "commands.log")
	source := fmt.Sprintf(`package main
import("bufio";"fmt";"os";"strings")
func main(){
 args:=os.Args[1:]; f,_:=os.OpenFile(%q,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); fmt.Fprintln(f,strings.Join(args," ")); f.Close()
 if len(args)==0{return}; if args[0]=="inspect" {fmt.Println("172.20.0.2"); return}; if args[0]!="run"{return}
 network,proxy:="",""; for i:=0;i+1<len(args);i++{if args[i]=="--network"{network=args[i+1]};if args[i]=="--env"&&strings.HasPrefix(args[i+1],"HTTPS_PROXY="){proxy=args[i+1]}}
 bridge:=strings.HasPrefix(network,"container:hermes-preflight-net-")
 safe:=bridge||(strings.HasPrefix(network,"hermes-probe-")&&proxy=="HTTPS_PROXY=http://172.20.0.2:3128")
 for _,arg:=range args{if arg=="-d" {if !safe{fmt.Fprintln(os.Stderr,"credentialed server escaped managed network");os.Exit(1)};return}}
 reader:=bufio.NewReader(os.Stdin);for i:=0;i<3;i++{if _,err:=reader.ReadString('\n');err!=nil{return}}
 if !safe {fmt.Println(`+"`"+`{"id":2,"error":{"message":"credentialed probe escaped managed network"}}`+"`"+`);return}
 fmt.Println(`+"`"+`{"id":2,"result":{"tools":[{"name":"search","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}}`+"`"+`)
}`, logPath)
	file := filepath.Join(root, "docker.go")
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "docker")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, file).CombinedOutput(); err != nil {
		t.Fatalf("build Docker test helper: %v %s", err, output)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := ToolDefinition{Source: DefinitionSource{Image: "ghcr.io/example/custom-mcp", Digest: "sha256:" + strings.Repeat("a", 64), Repository: "https://github.com/example/custom-mcp"}, Credentials: []CredentialInput{{Name: "CUSTOM_TOKEN", Required: true}}, Execution: ExecutionPolicy{Egress: []string{"api.example.com"}}}
	ctx := WithProbeOptions(t.Context(), ProbeOptions{Values: map[string]string{"CUSTOM_TOKEN": "synthetic"}, AllowNetwork: true})
	tools, err := ListMCPToolsFromLocalImage(ctx, ImportedArtifact{Definition: d})
	if err != nil || len(tools) != 1 {
		t.Errorf("generic credentialed probe lacks managed network: %v", err)
	}
	d.Source.Args = []string{"--transport", "sse"}
	tools, err = NetworkProbeToolList(ctx, ImportedArtifact{Definition: d})
	if err != nil || len(tools) != 1 {
		t.Errorf("generic network-transport probe lacks managed network: %v", err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"network create --internal", "network rm hermes-probe-", "volume rm hermes-probe-"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing probe network lifecycle: %s", want)
		}
	}
}
