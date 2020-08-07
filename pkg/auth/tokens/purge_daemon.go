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
)

const (
	tokenPurgeInterval = "@every 0h30m0s"
)

type purger struct {
	tokenLister      v3.TokenLister
	tokens           v3.TokenInterface
	samlTokens       v3.SamlTokenInterface
	samlTokensLister v3.SamlTokenLister
}

var (
	p *purger
	c = cron.New()
)

func StartPurgeDaemon(ctx context.Context, mgmt *config.ManagementContext) {
	p = &purger{
		tokenLister: mgmt.Management.Tokens("").Controller().Lister(),
		tokens:      mgmt.Management.Tokens(""),
	}

	p.purge()
}

func (p *purger) purge() {
	parsed, err := cron.ParseStandard(tokenPurgeInterval)
	if err != nil {
		logrus.Errorf("Error parsing token purge interval [%v]", err)
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
	if p == nil {
		return
	}

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

	// saml tokens store encrypted token for login request from rancher cli
	samlTokens, err := p.samlTokensLister.List(namespace.GlobalNamespace, labels.Everything())
	if err != nil {
		return
	}

	count = 0
	for _, token := range samlTokens {
		// avoid delete immediately after creation, login request might be pending
		if token.CreationTimestamp.Add(15 * time.Minute).Before(time.Now()) {
			err = p.samlTokens.Delete(token.ObjectMeta.Name, &metav1.DeleteOptions{})
			if err != nil && !clientbase.IsNotFound(err) {
				logrus.Errorf("Error: while deleting expired token %v: %v", err, token.Name)
				continue
			}
			count++
		}
	}
	if count > 0 {
		logrus.Infof("Purged %v saml tokens", count)
	}
}
