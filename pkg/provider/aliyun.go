package provider

import (
	"fmt"
	"strings"
	"sync"
	"time"

	alidns "github.com/alibabacloud-go/alidns-20150109/v4/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	pvtz "github.com/alibabacloud-go/pvtz-20180101/v2/client"
	sts "github.com/alibabacloud-go/sts-20150401/v2/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/golang-module/carbon/v2"

	"github.com/bryant-rh/cloud_dns_exporter/pkg/public/logger"

	"github.com/bryant-rh/cloud_dns_exporter/pkg/public"
)

type AliyunDNS struct {
	account          public.Account
	client           *alidns.Client
	pvtzClient       *pvtz.Client
	stsCredentials   *STSCredentials
	credentialsMutex sync.RWMutex
}

// STSCredentials STS 临时凭证
type STSCredentials struct {
	AccessKeyId     string
	AccessKeySecret string
	SecurityToken   string
	Expiration      time.Time
}

// IsExpired 检查凭证是否过期
func (c *STSCredentials) IsExpired() bool {
	return time.Now().After(c.Expiration.Add(-5 * time.Minute)) // 提前5分钟刷新
}

// NewAliyunClient 初始化客户端
func NewAliyunClient(secretID, secretKey string) (*alidns.Client, error) {
	config := openapi.Config{
		AccessKeyId:     tea.String(secretID),
		AccessKeySecret: tea.String(secretKey),
	}
	config.Endpoint = tea.String("dns.aliyuncs.com")
	client, err := alidns.NewClient(&config)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// NewAliyunClientWithSTS 使用 STS 凭证初始化客户端
func NewAliyunClientWithSTS(accessKeyId, accessKeySecret, securityToken string) (*alidns.Client, error) {
	config := openapi.Config{
		AccessKeyId:     tea.String(accessKeyId),
		AccessKeySecret: tea.String(accessKeySecret),
		SecurityToken:   tea.String(securityToken),
	}
	// config.Endpoint = tea.String("dns.aliyuncs.com")
	config.Endpoint = tea.String("alidns.aliyuncs.com")
	client, err := alidns.NewClient(&config)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// NewAliyunPVTZClient 初始化私网DNS客户端
func NewAliyunPVTZClient(secretID, secretKey, region string) (*pvtz.Client, error) {
	config := openapi.Config{
		AccessKeyId:     tea.String(secretID),
		AccessKeySecret: tea.String(secretKey),
	}
	// 私网DNS需要指定地域
	// if region == "" {
	// 	region = "cn-hangzhou" // 默认杭州
	// }
	// config.Endpoint = tea.String(fmt.Sprintf("pvtz.%s.aliyuncs.com", region))
	config.Endpoint = tea.String("pvtz.aliyuncs.com")
	client, err := pvtz.NewClient(&config)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// NewAliyunPVTZClientWithSTS 使用 STS 凭证初始化私网DNS客户端
func NewAliyunPVTZClientWithSTS(accessKeyId, accessKeySecret, securityToken, region string) (*pvtz.Client, error) {
	config := openapi.Config{
		AccessKeyId:     tea.String(accessKeyId),
		AccessKeySecret: tea.String(accessKeySecret),
		SecurityToken:   tea.String(securityToken),
	}
	// 私网DNS需要指定地域
	if region == "" {
		region = "ap-northeast-1" // 默认杭州
	}
	config.Endpoint = tea.String(fmt.Sprintf("pvtz.%s.aliyuncs.com", region))
	client, err := pvtz.NewClient(&config)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// assumeRole 使用 STS 获取临时凭证
func (a *AliyunDNS) assumeRole() (*STSCredentials, error) {
	if a.account.RoleArn == "" {
		return nil, fmt.Errorf("roleArn 未配置")
	}

	// 创建 STS 客户端
	config := openapi.Config{
		AccessKeyId:     tea.String(a.account.SecretID),
		AccessKeySecret: tea.String(a.account.SecretKey),
	}
	config.Endpoint = tea.String("sts.aliyuncs.com")

	stsClient, err := sts.NewClient(&config)
	if err != nil {
		return nil, fmt.Errorf("创建 STS 客户端失败: %v", err)
	}

	// 调用 AssumeRole
	// RoleSessionName 使用简单格式，参考 shouwang 项目
	sessionName := "dns_exporter_session"

	request := &sts.AssumeRoleRequest{
		RoleArn:         tea.String(a.account.RoleArn),
		RoleSessionName: tea.String(sessionName),
		DurationSeconds: tea.Int64(3600), // 1小时
	}

	response, err := stsClient.AssumeRole(request)
	if err != nil {
		return nil, fmt.Errorf("AssumeRole 失败: %v", err)
	}

	if response.Body == nil || response.Body.Credentials == nil {
		return nil, fmt.Errorf("STS 响应无效")
	}

	creds := response.Body.Credentials
	expiration, err := time.Parse(time.RFC3339, tea.StringValue(creds.Expiration))
	if err != nil {
		return nil, fmt.Errorf("解析过期时间失败: %v", err)
	}

	return &STSCredentials{
		AccessKeyId:     tea.StringValue(creds.AccessKeyId),
		AccessKeySecret: tea.StringValue(creds.AccessKeySecret),
		SecurityToken:   tea.StringValue(creds.SecurityToken),
		Expiration:      expiration,
	}, nil
}

// getValidCredentials 获取有效的凭证（自动刷新）
func (a *AliyunDNS) getValidCredentials() (*STSCredentials, error) {
	// 如果没有配置 STS，返回 nil
	if a.account.RoleArn == "" {
		return nil, nil
	}

	a.credentialsMutex.RLock()
	if a.stsCredentials != nil && !a.stsCredentials.IsExpired() {
		defer a.credentialsMutex.RUnlock()
		return a.stsCredentials, nil
	}
	a.credentialsMutex.RUnlock()

	// 需要刷新凭证
	a.credentialsMutex.Lock()
	defer a.credentialsMutex.Unlock()

	// 双重检查
	if a.stsCredentials != nil && !a.stsCredentials.IsExpired() {
		return a.stsCredentials, nil
	}

	// 获取新凭证
	newCreds, err := a.assumeRole()
	if err != nil {
		return nil, err
	}

	a.stsCredentials = newCreds
	logger.Info(fmt.Sprintf("STS 凭证已刷新，过期时间: %s", newCreds.Expiration.Format(time.RFC3339)))

	return newCreds, nil
}

// createDNSClient 创建DNS客户端（支持STS）
func (a *AliyunDNS) createDNSClient() (*alidns.Client, error) {
	creds, err := a.getValidCredentials()
	if err != nil {
		return nil, fmt.Errorf("获取STS凭证失败: %v", err)
	}

	if creds != nil {
		// 使用 STS 凭证
		return NewAliyunClientWithSTS(creds.AccessKeyId, creds.AccessKeySecret, creds.SecurityToken)
	} else {
		// 使用原始凭证
		return NewAliyunClient(a.account.SecretID, a.account.SecretKey)
	}
}

// createPVTZClient 创建私网DNS客户端（支持STS）
func (a *AliyunDNS) createPVTZClient(region string) (*pvtz.Client, error) {
	creds, err := a.getValidCredentials()
	if err != nil {
		return nil, fmt.Errorf("获取STS凭证失败: %v", err)
	}

	if creds != nil {
		// 使用 STS 凭证
		return NewAliyunPVTZClientWithSTS(creds.AccessKeyId, creds.AccessKeySecret, creds.SecurityToken, region)
	} else {
		// 使用原始凭证
		return NewAliyunPVTZClient(a.account.SecretID, a.account.SecretKey, region)
	}
}

// NewAliyunDNS 创建实例
func NewAliyunDNS(account public.Account) (*AliyunDNS, error) {
	aliyunDNS := &AliyunDNS{
		account: account,
	}

	// 初始化客户端
	client, err := aliyunDNS.createDNSClient()
	if err != nil {
		return nil, err
	}
	aliyunDNS.client = client

	return aliyunDNS, nil
}

// ListDomains 获取域名列表（公网+内网）
func (a *AliyunDNS) ListDomains() ([]Domain, error) {
	var allDomains []Domain

	// 1. 采集公网域名
	publicDomains, err := a.listPublicDomains()
	if err != nil {
		logger.Error(fmt.Sprintf("采集公网域名失败: %v", err))
		// 不中断流程，继续采集内网域名
	} else {
		allDomains = append(allDomains, publicDomains...)
		logger.Info(fmt.Sprintf("采集到 %d 个公网域名", len(publicDomains)))
	}

	// 2. 根据配置决定是否采集内网域名
	var privateDomains []Domain
	if a.account.EnablePrivateDNS {
		privateDomains, err = a.listPrivateDomains()
		if err != nil {
			logger.Error(fmt.Sprintf("采集内网域名失败: %v", err))
			// 不中断流程
		} else {
			allDomains = append(allDomains, privateDomains...)
			logger.Info(fmt.Sprintf("采集到 %d 个内网域名", len(privateDomains)))
		}
	} else {
		logger.Info("内网域名监控已禁用，跳过内网域名采集")
	}

	logger.Info(fmt.Sprintf("总共采集到 %d 个域名（公网: %d, 内网: %d）",
		len(allDomains), len(publicDomains), len(privateDomains)))

	return allDomains, nil
}

// listPublicDomains 采集公网域名
func (a *AliyunDNS) listPublicDomains() ([]Domain, error) {
	client, err := a.createDNSClient()
	if err != nil {
		return nil, err
	}

	var allDomains []Domain
	pageNumber := int64(1)
	pageSize := int64(100)

	for {
		request := &alidns.DescribeDomainsRequest{
			PageNumber: tea.Int64(pageNumber),
			PageSize:   tea.Int64(pageSize),
		}

		response, err := client.DescribeDomains(request)
		if err != nil {
			return nil, fmt.Errorf("查询公网DNS域名失败: %v", err)
		}

		if response.Body.Domains == nil || len(response.Body.Domains.Domain) == 0 {
			break
		}

		// 处理每个域名
		for _, domain := range response.Body.Domains.Domain {
			// 转换为通用域名结构
			d := a.convertPublicDomainToCommon(domain)
			allDomains = append(allDomains, d)
		}

		// 检查是否还有更多页
		if len(response.Body.Domains.Domain) < int(pageSize) {
			break
		}
		pageNumber++
	}

	return allDomains, nil
}

// listPrivateDomains 采集内网域名
func (a *AliyunDNS) listPrivateDomains() ([]Domain, error) {
	// 私网DNS需要指定地域，这里使用杭州作为默认地域
	client, err := a.createPVTZClient("cn-hangzhou")
	if err != nil {
		return nil, err
	}

	var allDomains []Domain
	pageNumber := int32(1)
	pageSize := int32(100)

	for {
		request := &pvtz.DescribeZonesRequest{
			PageNumber: tea.Int32(pageNumber),
			PageSize:   tea.Int32(pageSize),
		}

		response, err := client.DescribeZones(request)
		if err != nil {
			return nil, fmt.Errorf("查询内网DNS域名失败: %v", err)
		}

		if response.Body.Zones == nil || len(response.Body.Zones.Zone) == 0 {
			break
		}

		// 处理每个内网域名
		for _, zone := range response.Body.Zones.Zone {
			// 转换为通用域名结构
			d := a.convertPrivateDomainToCommon(zone)
			allDomains = append(allDomains, d)
		}

		// 检查是否还有更多页
		if len(response.Body.Zones.Zone) < int(pageSize) {
			break
		}
		pageNumber++
	}

	return allDomains, nil
}

// convertPublicDomainToCommon 转换公网域名为通用结构
func (a *AliyunDNS) convertPublicDomainToCommon(domain *alidns.DescribeDomainsResponseBodyDomainsDomain) Domain {
	// 解析创建时间
	var createdDate string
	if domain.CreateTime != nil && *domain.CreateTime != "" {
		if t, err := time.Parse("2006-01-02T15:04Z", *domain.CreateTime); err == nil {
			createdDate = t.Format("2006-01-02 15:04:05")
		}
	}

	domainName := tea.StringValue(domain.DomainName)

	return Domain{
		CloudProvider:   a.account.CloudProvider,
		CloudName:       a.account.CloudName,
		DomainID:        fmt.Sprintf("public_%s", domainName), // 公网域名ID前缀
		DomainName:      domainName,
		DomainType:      "public", // 公网域名类型
		DomainRemark:    tea.StringValue(domain.Remark),
		DomainStatus:    "normal", // 阿里云公网域名默认正常
		CreatedDate:     createdDate,
		ExpiryDate:      "", // 公网域名没有过期时间概念
		DaysUntilExpiry: 0,  // 公网域名不计算过期天数
	}
}

// convertPrivateDomainToCommon 转换内网域名为通用结构
func (a *AliyunDNS) convertPrivateDomainToCommon(zone *pvtz.DescribeZonesResponseBodyZonesZone) Domain {
	// 解析创建时间
	var createdDate string
	if zone.CreateTime != nil && *zone.CreateTime != "" {
		// 尝试多种时间格式
		timeFormats := []string{
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05.000Z",
			"2006-01-02 15:04:05",
			time.RFC3339,
		}
		for _, format := range timeFormats {
			if t, err := time.Parse(format, *zone.CreateTime); err == nil {
				createdDate = t.Format("2006-01-02 15:04:05")
				break
			}
		}
	}

	zoneId := tea.StringValue(zone.ZoneId)
	zoneName := tea.StringValue(zone.ZoneName)

	return Domain{
		CloudProvider:   a.account.CloudProvider,
		CloudName:       a.account.CloudName,
		DomainID:        fmt.Sprintf("private_%s", zoneId), // 内网域名ID前缀
		DomainName:      zoneName,
		DomainType:      "private", // 内网域名类型
		DomainRemark:    tea.StringValue(zone.Remark),
		DomainStatus:    "normal", // 内网域名默认正常
		CreatedDate:     createdDate,
		ExpiryDate:      "", // 内网域名没有过期时间概念
		DaysUntilExpiry: 0,  // 内网域名不计算过期天数
	}
}

// ListRecords 获取记录列表（公网+内网）
func (a *AliyunDNS) ListRecords() ([]Record, error) {
	var allRecords []Record

	// 1. 采集公网DNS记录
	publicRecords, err := a.listPublicRecords()
	if err != nil {
		logger.Error(fmt.Sprintf("采集公网DNS记录失败: %v", err))
		// 不中断流程，继续采集内网记录
	} else {
		allRecords = append(allRecords, publicRecords...)
		logger.Info(fmt.Sprintf("采集到 %d 条公网DNS记录", len(publicRecords)))
	}

	// 2. 根据配置决定是否采集内网DNS记录
	var privateRecords []Record
	if a.account.EnablePrivateDNS {
		privateRecords, err = a.listPrivateRecords()
		if err != nil {
			logger.Error(fmt.Sprintf("采集内网DNS记录失败: %v", err))
			// 不中断流程
		} else {
			allRecords = append(allRecords, privateRecords...)
			logger.Info(fmt.Sprintf("采集到 %d 条内网DNS记录", len(privateRecords)))
		}
	} else {
		logger.Info("内网域名监控已禁用，跳过内网DNS记录采集")
	}

	logger.Info(fmt.Sprintf("总共采集到 %d 条DNS记录（公网: %d, 内网: %d）",
		len(allRecords), len(publicRecords), len(privateRecords)))

	return allRecords, nil
}

// listPublicRecords 采集公网DNS记录
func (a *AliyunDNS) listPublicRecords() ([]Record, error) {
	// 首先获取所有公网域名
	publicDomains, err := a.listPublicDomains()
	if err != nil {
		return nil, fmt.Errorf("获取公网域名列表失败: %v", err)
	}

	client, err := a.createDNSClient()
	if err != nil {
		return nil, err
	}

	var allRecords []Record

	// 为每个公网域名获取DNS记录
	for _, domain := range publicDomains {
		records, err := a.getDomainRecords(client, domain.DomainName, domain.DomainID, "public")
		if err != nil {
			logger.Error(fmt.Sprintf("获取域名 %s 的DNS记录失败: %v", domain.DomainName, err))
			continue // 继续处理下一个域名
		}
		allRecords = append(allRecords, records...)
	}

	return allRecords, nil
}

// listPrivateRecords 采集内网DNS记录
func (a *AliyunDNS) listPrivateRecords() ([]Record, error) {
	// 首先获取所有内网域名
	privateDomains, err := a.listPrivateDomains()
	if err != nil {
		return nil, fmt.Errorf("获取内网域名列表失败: %v", err)
	}

	client, err := a.createPVTZClient("cn-hangzhou")
	if err != nil {
		return nil, err
	}

	var allRecords []Record

	// 为每个内网域名获取DNS记录
	for _, domain := range privateDomains {
		// 从 domain.DomainID 中提取 zoneId (格式: private_<zoneId>)
		zoneId := strings.TrimPrefix(domain.DomainID, "private_")
		records, err := a.getPrivateZoneRecords(client, zoneId, domain.DomainName, domain.DomainID, "private")
		if err != nil {
			logger.Error(fmt.Sprintf("获取内网域名 %s 的DNS记录失败: %v", domain.DomainName, err))
			continue // 继续处理下一个域名
		}
		allRecords = append(allRecords, records...)
	}

	return allRecords, nil
}

// getDomainRecords 获取公网域名的DNS记录
func (a *AliyunDNS) getDomainRecords(client *alidns.Client, domainName, domainID, domainType string) ([]Record, error) {
	var allRecords []Record
	pageNumber := int64(1)
	pageSize := int64(500)

	for {
		request := &alidns.DescribeDomainRecordsRequest{
			DomainName: tea.String(domainName),
			PageNumber: tea.Int64(pageNumber),
			PageSize:   tea.Int64(pageSize),
		}

		response, err := client.DescribeDomainRecords(request)
		if err != nil {
			return nil, fmt.Errorf("查询域名 %s 的DNS记录失败: %v", domainName, err)
		}

		if response.Body.DomainRecords == nil || len(response.Body.DomainRecords.Record) == 0 {
			break
		}

		// 处理每条记录
		for _, record := range response.Body.DomainRecords.Record {
			r := a.convertPublicRecordToCommon(record, domainName, domainID, domainType)
			allRecords = append(allRecords, r)
		}

		// 检查是否还有更多页
		if len(response.Body.DomainRecords.Record) < int(pageSize) {
			break
		}
		pageNumber++
	}

	return allRecords, nil
}

// getPrivateZoneRecords 获取内网域名的DNS记录
func (a *AliyunDNS) getPrivateZoneRecords(client *pvtz.Client, zoneId, domainName, domainID, domainType string) ([]Record, error) {
	var allRecords []Record
	pageNumber := int32(1)
	pageSize := int32(100)

	for {
		request := &pvtz.DescribeZoneRecordsRequest{
			ZoneId:     tea.String(zoneId),
			PageNumber: tea.Int32(pageNumber),
			PageSize:   tea.Int32(pageSize),
		}

		response, err := client.DescribeZoneRecords(request)
		if err != nil {
			return nil, fmt.Errorf("查询内网域名 %s 的DNS记录失败: %v", domainName, err)
		}

		if response.Body.Records == nil || len(response.Body.Records.Record) == 0 {
			break
		}

		// 处理每条记录
		for _, record := range response.Body.Records.Record {
			r := a.convertPrivateRecordToCommon(record, domainName, domainID, domainType)
			allRecords = append(allRecords, r)
		}

		// 检查是否还有更多页
		if len(response.Body.Records.Record) < int(pageSize) {
			break
		}
		pageNumber++
	}

	return allRecords, nil
}

// convertPublicRecordToCommon 转换公网DNS记录为通用结构
func (a *AliyunDNS) convertPublicRecordToCommon(record *alidns.DescribeDomainRecordsResponseBodyDomainRecordsRecord, domainName, domainID, domainType string) Record {
	recordID := tea.StringValue(record.RecordId)
	recordType := tea.StringValue(record.Type)
	rr := tea.StringValue(record.RR)
	value := tea.StringValue(record.Value)
	ttl := tea.Int64Value(record.TTL)
	status := tea.StringValue(record.Status)
	weight := tea.Int32Value(record.Weight)

	// 转换状态
	var recordStatus string
	if strings.ToLower(status) == "enable" {
		recordStatus = "enable"
	} else {
		recordStatus = "disable"
	}

	// 构建完整记录名
	var fullRecord string
	if rr == "@" || rr == "" {
		fullRecord = domainName
	} else {
		fullRecord = fmt.Sprintf("%s.%s", rr, domainName)
	}

	// 格式化更新时间
	var updateTime string
	if record.UpdateTimestamp != nil {
		updateTime = carbon.CreateFromTimestampMilli(tea.Int64Value(record.UpdateTimestamp)).ToDateTimeString()
	}

	return Record{
		CloudProvider: a.account.CloudProvider,
		CloudName:     a.account.CloudName,
		DomainName:    domainName,
		DomainType:    domainType,
		RecordID:      recordID,
		RecordType:    recordType,
		RecordName:    rr,
		RecordValue:   value,
		RecordTTL:     fmt.Sprintf("%d", ttl),
		RecordWeight:  fmt.Sprintf("%d", weight),
		RecordStatus:  recordStatus,
		RecordRemark:  tea.StringValue(record.Remark),
		UpdateTime:    updateTime,
		FullRecord:    fullRecord,
	}
}

// convertPrivateRecordToCommon 转换内网DNS记录为通用结构
func (a *AliyunDNS) convertPrivateRecordToCommon(record *pvtz.DescribeZoneRecordsResponseBodyRecordsRecord, domainName, domainID, domainType string) Record {
	recordID := fmt.Sprintf("%d", tea.Int64Value(record.RecordId))
	recordType := tea.StringValue(record.Type)
	rr := tea.StringValue(record.Rr)
	value := tea.StringValue(record.Value)
	ttl := tea.Int32Value(record.Ttl)
	status := tea.StringValue(record.Status)
	weight := tea.Int32Value(record.Weight)

	// 转换状态
	var recordStatus string
	if strings.ToLower(status) == "enable" {
		recordStatus = "enable"
	} else {
		recordStatus = "disable"
	}

	// 构建完整记录名
	var fullRecord string
	if rr == "@" || rr == "" {
		fullRecord = domainName
	} else {
		fullRecord = fmt.Sprintf("%s.%s", rr, domainName)
	}

	return Record{
		CloudProvider: a.account.CloudProvider,
		CloudName:     a.account.CloudName,
		DomainName:    domainName,
		DomainType:    domainType,
		RecordID:      recordID,
		RecordType:    recordType,
		RecordName:    rr,
		RecordValue:   value,
		RecordTTL:     fmt.Sprintf("%d", ttl),
		RecordWeight:  fmt.Sprintf("%d", weight),
		RecordStatus:  recordStatus,
		RecordRemark:  tea.StringValue(record.Remark),
		UpdateTime:    "", // 内网DNS记录没有更新时间字段
		FullRecord:    fullRecord,
	}
}
