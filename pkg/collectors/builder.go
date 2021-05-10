package collectors

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kube-state-metrics/pkg/metric"
	"k8s.io/kube-state-metrics/pkg/options"

	"golang.org/x/net/context"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/kube-state-metrics/pkg/collector"
	metricsstore "k8s.io/kube-state-metrics/pkg/metrics_store"
)

type whiteBlackLister interface {
	IsIncluded(string) bool
	IsExcluded(string) bool
}

// Builder helps to build collectors. It follows the builder pattern
// (https://en.wikipedia.org/wiki/Builder_pattern).
type Builder struct {
	apiserver         string
	kubeconfig        string
	namespaces        options.NamespaceList
	ctx               context.Context
	enabledCollectors []string
	whiteBlackList    whiteBlackLister
}

// NewBuilder returns a new builder.
func NewBuilder(
	ctx context.Context,
) *Builder {
	return &Builder{
		ctx: ctx,
	}
}

func (b *Builder) WithApiserver(apiserver string) *Builder {
	b.apiserver = apiserver
	return b
}

func (b *Builder) WithKubeConfig(kubeconfig string) *Builder {
	b.kubeconfig = kubeconfig
	return b
}

// WithEnabledCollectors sets the enabledCollectors property of a Builder.
func (b *Builder) WithEnabledCollectors(c []string) *Builder {
	copy := []string{}

	copy = append(copy, c...)

	sort.Strings(copy)

	b.enabledCollectors = copy
	return b
}

// WithNamespaces sets the namespaces property of a Builder.
func (b *Builder) WithNamespaces(n options.NamespaceList) *Builder {
	b.namespaces = n
	return b
}

// WithWhiteBlackList configures the white or blacklisted metrics to be exposed
// by the collectors build by the Builder
func (b *Builder) WithWhiteBlackList(l whiteBlackLister) *Builder {
	b.whiteBlackList = l
	return b
}

// Build initializes and registers all enabled collectors.
func (b *Builder) Build() []*collector.Collector {
	if b.whiteBlackList == nil {
		panic("whiteBlackList should not be nil")
	}

	collectors := []*collector.Collector{}
	activeCollectorNames := []string{}

	for _, c := range b.enabledCollectors {
		constructor, ok := availableCollectors[c]
		if !ok {
			klog.Fatalf("collector %s is not correct", c)
		}

		collector := constructor(b)
		activeCollectorNames = append(activeCollectorNames, c)
		collectors = append(collectors, collector)

	}

	klog.Infof("Active collectors: %s", strings.Join(activeCollectorNames, ","))

	return collectors
}

var availableCollectors = map[string]func(f *Builder) *collector.Collector{
	"policyreports": func(b *Builder) *collector.Collector { return b.buildPolicyReportCollector() },
}

func (b *Builder) buildPolicyReportCollector() *collector.Collector {
	config, err := clientcmd.BuildConfigFromFlags(b.apiserver, b.kubeconfig)
	if err != nil {
		klog.Fatalf("cannot create Dynamic client: %v", err)
	}
	client := dynamic.NewForConfigOrDie(config)
	return b.buildPolicyReportCollectorWithClient(client)
}

func (b *Builder) buildPolicyReportCollectorWithClient(client dynamic.Interface) *collector.Collector {
	filteredMetricFamilies := metric.FilterMetricFamilies(b.whiteBlackList,
		getPolicyReportMetricFamilies(client))
	composedMetricGenFuncs := metric.ComposeMetricGenFuncs(filteredMetricFamilies)

	familyHeaders := metric.ExtractMetricFamilyHeaders(filteredMetricFamilies)

	store := metricsstore.NewMetricsStore(
		familyHeaders,
		composedMetricGenFuncs,
	)
	reflectorPerNamespace(b.ctx, &unstructured.Unstructured{}, store,
		b.apiserver, b.kubeconfig, b.namespaces, createPolicyReportListWatch)

	return collector.NewCollector(store)
}

// reflectorPerNamespace creates a Kubernetes client-go reflector with the given
// listWatchFunc for each given namespace and registers it with the given store.
func reflectorPerNamespace(
	ctx context.Context,
	expectedType interface{},
	store cache.Store,
	apiserver string,
	kubeconfig string,
	namespaces []string,
	listWatchFunc func(apiserver string, kubeconfig string, ns string) cache.ListWatch,
) {
	for _, ns := range namespaces {
		lw := listWatchFunc(apiserver, kubeconfig, ns)
		reflector := cache.NewReflector(&lw, expectedType, store, 0)
		go reflector.Run(ctx.Done())
	}
}
