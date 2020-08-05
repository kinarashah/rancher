package tokens

import (
	"context"
	"time"

	"github.com/rancher/norman/clientbase"
	"github.com/rancher/rancher/pkg/namespace"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/robfig/cron"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	intervalSeconds   int64 = 3600
	samlTokenInterval       = "@every 0h30m0s"
)

var (
	t *samlTokenPurger
	c = cron.New()
)

func StartPurgeDaemon(ctx context.Context, mgmt *config.ManagementContext) {
	p := &purger{
		tokenLister: mgmt.Management.Tokens("").Controller().Lister(),
		tokens:      mgmt.Management.Tokens(""),
	}

	t = &samlTokenPurger{
		samlTokens:       mgmt.Management.SamlTokens(""),
		samlTokensLister: mgmt.Management.SamlTokens("").Controller().Lister(),
	}

	go wait.JitterUntil(p.purge, time.Duration(intervalSeconds)*time.Second, .1, true, ctx.Done())

	t.purgeSamlTokens()
}

type purger struct {
	tokenLister v3.TokenLister
	tokens      v3.TokenInterface
}

func (p *purger) purge() {
	allTokens, err := p.tokenLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("Error listing tokens during purge: %v", err)
	}

	var count int
	for _, token := range allTokens {
		if IsExpired(*token) {
			err = p.tokens.Delete(token.ObjectMeta.Name, &metav1.DeleteOptions{})
			if err != nil && !clientbase.IsNotFound(err) {
				logrus.Errorf("Error: while deleting expired token %v: %v", err, token.ObjectMeta.Name)
				continue
			}
			count++
		}
	}
	if count > 0 {
		logrus.Infof("Purged %v expired tokens", count)
	}
}

// saml tokens store encrypted token for login request from rancher cli
type samlTokenPurger struct {
	samlTokens       v3.SamlTokenInterface
	samlTokensLister v3.SamlTokenLister
}

func (t *samlTokenPurger) purgeSamlTokens() {
	parsed, err := cron.ParseStandard(samlTokenInterval)
	if err != nil {
		logrus.Errorf("Error parsing saml token cron [%v]", err)
		return
	}
	c.Stop()
	c = cron.New()

	if parsed != nil {
		job := cron.FuncJob(purge)
		c.Schedule(parsed, job)
		c.Start()
	}
}

func purge() {
	if t == nil {
		return
	}

	tokens, err := t.samlTokensLister.List(namespace.GlobalNamespace, labels.Everything())
	if err != nil {
		return
	}

	var count int
	for _, token := range tokens {
		// avoid delete immediately after creation, login request might be pending
		if token.CreationTimestamp.Add(15 * time.Minute).Before(time.Now()) {
			err = t.samlTokens.Delete(token.ObjectMeta.Name, &metav1.DeleteOptions{})
			if err != nil && !clientbase.IsNotFound(err) {
				logrus.Errorf("Error: while deleting expired token %v: %v", err, token.Name)
				continue
			}
			count++
		}
	}
	if count > 0 {
		logrus.Debugf("[SamlTokenPurger] Purged %v saml tokens", count)
	}
}
