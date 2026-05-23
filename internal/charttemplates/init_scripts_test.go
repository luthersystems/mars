package charttemplates

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitScriptsCopyConfigIntoExistingDestinations(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		copies []string
	}{
		{
			name:   "connectorhub crypto config",
			path:   "ansible-roles/helm_charts/files/helmcharts/connectorhub/templates/deployment.yaml",
			copies: []string{"cp -a crypto-config/. /fabric/crypto-config/"},
		},
		{
			name:   "shiroclient crypto config",
			path:   "ansible-roles/helm_charts/files/helmcharts/shiroclient/templates/deployment.yaml",
			copies: []string{"cp -a crypto-config/. /fabric/crypto-config/"},
		},
		{
			name: "fabric cli config",
			path: "ansible-roles/k8s_fabric_cli/files/fabric-cli/templates/deployment.yaml",
			copies: []string{
				"cp -a channel-artifacts/. /channel-artifacts/",
				"cp -a crypto-config/ordererOrganizations/{{ .Values.dlt.domain }}/orderers/orderer0.{{ .Values.dlt.domain }}/msp/tlscacerts/. /orderertls/",
				"cp -a ordererOrganizations/{{ .Values.dlt.domain }}/users/Admin@{{ .Values.dlt.domain }}/msp/. /msp/",
				"cp -a ordererOrganizations/{{ .Values.dlt.domain }}/orderers/orderer{{ .Values.dlt.peerIndex }}.{{ .Values.dlt.domain }}/tls/. /tls/",
				"cp -a peerOrganizations/{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/users/Admin@{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/msp/. /msp/",
				"cp -a peerOrganizations/{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/peers/{{ list .Values.dlt.peerIndex . | include \"fabric-cli.peer-fqdn\" }}/tls/. /tls/",
			},
		},
		{
			name: "fabric orderer config",
			path: "ansible-roles/k8s_fabric_orderer/files/fabric-orderer/templates/deployment.yaml",
			copies: []string{
				"cp -a channel-artifacts/. /channel-artifacts/",
				"cp -a ordererOrganizations/{{ .Values.dlt.domain }}/orderers/{{ include \"fabric-orderer.self-fqdn\" . }}/msp/. /msp/",
				"cp -a ordererOrganizations/{{ .Values.dlt.domain }}/orderers/{{ include \"fabric-orderer.self-fqdn\" . }}/tls/. /tls/",
			},
		},
		{
			name: "fabric peer config",
			path: "ansible-roles/k8s_fabric_peer/files/fabric-peer/templates/deployment.yaml",
			copies: []string{
				"cp -a ordererOrganizations/{{ .Values.dlt.domain }}/orderers/orderer0.{{ .Values.dlt.domain }}/msp/tlscacerts/. /orderertls/",
				"cp -a peerOrganizations/{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/peers/{{ include \"fabric-peer.self-fqdn\" . }}/msp/. /msp/",
				"cp -a peerOrganizations/{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/peers/{{ include \"fabric-peer.self-fqdn\" . }}/tls/. /tls/",
				"cp -a peerOrganizations/{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/users/Admin@{{ .Values.dlt.organization }}.{{ .Values.dlt.domain }}/msp/. /adminmsp/",
			},
		},
		{
			name:   "fabric ca crypto config",
			path:   "ansible-roles/k8s_fabric_ca/files/fabric-ca/templates/deployment.yaml",
			copies: []string{"cp -a peerOrganizations/{{ .Values.dlt.domain }}/ca/. /crypto/"},
		},
	}

	root := repoRoot(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, tt.path))
			if err != nil {
				t.Fatal(err)
			}

			text := string(content)
			for _, copyCmd := range tt.copies {
				if !strings.Contains(text, copyCmd) {
					t.Fatalf("missing copy command %q", copyCmd)
				}
			}
			if strings.Contains(text, "rm -rf") {
				t.Fatal("init script should not delete destination mount contents")
			}
			if strings.Contains(text, "mv ") {
				t.Fatal("init script should copy into existing destination mounts instead of moving")
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
