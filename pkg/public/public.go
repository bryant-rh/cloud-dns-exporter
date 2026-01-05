package public

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/bryant-rh/cloud_dns_exporter/pkg/public/logger"
	"github.com/rs/xid"

	"gopkg.in/yaml.v2"
)

// InitSvc 初始化服务
func InitSvc() {
	LoadConfig()
	InitCache()
}

const (
	// Custom
	CustomRecords string = "custom_records"
	// Cloud Providers
	TencentDnsProvider    string = "tencent"
	AliyunDnsProvider     string = "aliyun"
	GodaddyDnsProvider    string = "godaddy"
	DNSLaDnsProvider      string = "dnsla"
	AmazonDnsProvider     string = "amazon"
	CloudFlareDnsProvider string = "cloudflare"
	// Metrics Name
	DomainList     string = "domain_list"
	RecordList     string = "record_list"
	RecordCertInfo string = "record_cert_info"
)

var (
	once      sync.Once
	Config    *Configuration
	Cache     *bigcache.BigCache
	CertCache *bigcache.BigCache
)

type Account struct {
	CloudProvider    string `yaml:"cloud_provider"`
	CloudName        string `yaml:"cloud_name"`
	SecretID         string `yaml:"secretId"`
	SecretKey        string `yaml:"secretKey"`
	RoleArn          string `yaml:"roleArn"`          // 新增：用于 STS 认证的 ARN
	EnablePrivateDNS bool   `yaml:"enablePrivateDNS"` // 新增：是否启用内网域名监控，默认false
}

// Config 表示配置文件的结构
type Configuration struct {
	CustomRecords  []string `yaml:"custom_records"`
	CloudProviders map[string]struct {
		Accounts []map[string]string `yaml:"accounts"`
	} `yaml:"cloud_providers"`
}

// LoadConfig 加载配置
func LoadConfig() *Configuration {
	once.Do(func() {
		Config = &Configuration{}
		data, err := os.ReadFile("config.yaml")
		if err != nil {
			logger.Fatal("read config file failed: ", err)
		}
		err = yaml.Unmarshal(data, &Config)
		if err != nil {
			logger.Fatal("unmarshal config file failed: ", err)
		}
	})
	return Config
}

// InitCache 初始化缓存
func InitCache() {
	var err error
	Cache, err = bigcache.New(context.Background(), bigcache.DefaultConfig(5*time.Minute))
	if err != nil {
		logger.Fatal("init cache failed: ", err)
	}
	CertCache, err = bigcache.New(context.Background(), bigcache.DefaultConfig(25*time.Hour))
	if err != nil {
		logger.Fatal("init cache failed: ", err)
	}
}

// GetID 获取唯一ID
func GetID() string {
	return xid.New().String()
}
