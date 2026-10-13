package grpc

// La identidad del stream sale SOLO del certificado mTLS del peer (peerIdentity; R-G7):
// CN = edge_id, Organization[0] = tenant_id, como los firma la CA de enrolamiento. Sin las dos
// cosas no hay identidad, y Connect degrada: nada de flota, lease ni config a nombre de nadie.
// El rechazo de un certificado ajeno o revocado es del transporte (mTLS real, F3-05).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func peerContext(info credentials.AuthInfo) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: info})
}

func tlsInfo(certs ...*x509.Certificate) credentials.TLSInfo {
	return credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: certs}}
}

func certFor(commonName string, organization ...string) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: commonName, Organization: organization}}
}

func TestPeerIdentityComesOnlyFromTheClientCertificate(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		ctx              context.Context
		tenantID, edgeID string
		ok               bool
	}{
		"edge certificate":                  {peerContext(tlsInfo(certFor("edge-1", "tenant-1"))), "tenant-1", "edge-1", true},
		"first organization is the tenant":  {peerContext(tlsInfo(certFor("edge-1", "tenant-1", "tenant-2"))), "tenant-1", "edge-1", true},
		"the leaf, not the rest of a chain": {peerContext(tlsInfo(certFor("edge-1", "tenant-1"), certFor("ca", "wapp"))), "tenant-1", "edge-1", true},
		"no peer":                           {context.Background(), "", "", false},
		"peer without auth info":            {peerContext(nil), "", "", false},
		"auth info that is not TLS":         {peerContext(notTLS{}), "", "", false},
		"TLS without client certificate":    {peerContext(tlsInfo()), "", "", false},
		"certificate without common name":   {peerContext(tlsInfo(certFor("", "tenant-1"))), "", "", false},
		"certificate without organization":  {peerContext(tlsInfo(certFor("edge-1"))), "", "", false},
		"certificate with empty tenant":     {peerContext(tlsInfo(certFor("edge-1", "", "tenant-2"))), "", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tenantID, edgeID, ok := peerIdentity(tc.ctx)
			if tenantID != tc.tenantID || edgeID != tc.edgeID || ok != tc.ok {
				t.Errorf("peerIdentity = (%q, %q, %v), se esperaba (%q, %q, %v)", tenantID, edgeID, ok, tc.tenantID, tc.edgeID, tc.ok)
			}
		})
	}
}

// notTLS es un AuthInfo de otro transporte.
type notTLS struct{}

func (notTLS) AuthType() string { return "insecure" }

// Visto desde Connect: un certificado al que le falta el tenant NO da una identidad a medias.
// El stream es anónimo y nada se escribe a nombre de un tenant vacío.
func TestConnectTreatsAnIncompleteCertificateAsNoIdentity(t *testing.T) {
	t.Parallel()
	for name, subject := range map[string][2]string{
		"without tenant": {"", "edge-1"},
		"without edge":   {"tenant-1", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
			edge := openStream(t, rig.srv, forgedIdentity(subject[0], subject[1]))

			edge.send(t, heartbeatFrame("s-1", fullHeartbeat()), loginFrame(control, "cmd-1"))
			edge.hangUp(t)

			if rig.tl.String() != "" || len(rig.srv.edgeSessions) != 0 || rig.authn.calls() != 0 {
				t.Errorf("un certificado incompleto dejó rastro: escrituras %q, seguimiento %v, llamadas a auth %d",
					rig.tl.String(), rig.srv.edgeSessions, rig.authn.calls())
			}
			configs, _, leases := splitFrames(t, edge.received())
			if len(configs) != 0 || len(leases) != 0 {
				t.Errorf("un stream sin identidad recibió %d configs y %d leases", len(configs), len(leases))
			}
		})
	}
}

// La identidad es la del certificado de CADA stream: dos Edge con el mismo edge_id bajo
// tenants distintos son dos Edge, y lo de uno no se escribe a nombre del otro.
func TestConnectScopesEverythingToTheTenantOfTheCertificate(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	mine := openStream(t, rig.srv, forgedIdentity("tenant-a", "edge-1"))
	theirs := openStream(t, rig.srv, forgedIdentity("tenant-b", "edge-1"))

	mine.send(t, pongFrame("s-a", 1))
	theirs.send(t, pongFrame("s-b", 1))

	if a, b := sortedSessions(rig.srv, "tenant-a", "edge-1"), sortedSessions(rig.srv, "tenant-b", "edge-1"); a != "s-a" || b != "s-b" {
		t.Errorf("seguimiento = (%q, %q), se esperaba cada sesión bajo el tenant de su certificado", a, b)
	}
	rig.row(t, phone("tenant-a", "edge-1", "s-a"))
	rig.row(t, phone("tenant-b", "edge-1", "s-b"))
	if _, found, err := rig.fleet.Get(t.Context(), "tenant-a", "edge-1", "s-b"); err != nil || found {
		t.Errorf("la sesión de un tenant aparece en la flota del otro (found=%v, err=%v)", found, err)
	}
}
