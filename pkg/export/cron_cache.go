package export

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/bryant-rh/cloud_dns_exporter/pkg/public/logger"
	"github.com/weppos/publicsuffix-go/publicsuffix"

	"github.com/bryant-rh/cloud_dns_exporter/pkg/provider"
	"github.com/bryant-rh/cloud_dns_exporter/pkg/public"
	"github.com/robfig/cron/v3"
)

// InitCron 初始化定时任务
func InitCron() {
	c := cron.New(cron.WithSeconds())
	// 域名采集：每5分钟执行一次
	_, _ = c.AddFunc("0 */5 * * * *", func() {
		loading()
	})

	// 证书采集：每小时执行一次
	_, _ = c.AddFunc("0 0 */1 * * *", func() {
		loadingCert()
		loadingCustomRecordCert()
	})

	// 启动时先执行域名采集，完成后再执行证书采集
	logger.Info("开始初始化数据采集...")
	loading() // 先执行域名采集
	logger.Info("域名数据采集完成，开始证书数据采集...")

	// 域名采集完成后立即执行证书采集
	loadingCert()
	loadingCustomRecordCert()
	logger.Info("初始化数据采集完成")

	c.Start()
}

func loading() {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for cloudProvider, accounts := range public.Config.CloudProviders {
		for _, cloudAccount := range accounts.Accounts {
			wg.Add(1)
			go func(cloudProvider, cloudName string, account map[string]string) {
				defer wg.Done()
				domainListCacheKey := public.DomainList + "_" + cloudProvider + "_" + cloudName
				dnsProvider, err := provider.Factory.Create(cloudProvider, account)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] create provider failed: %v", domainListCacheKey, err))
					return
				}
				domains, err := dnsProvider.ListDomains()
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] list domains failed: %v", domainListCacheKey, err))
					return
				}

				mu.Lock()
				value, err := json.Marshal(domains)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] marshal domain list failed: %v", domainListCacheKey, err))
				}
				if err := public.Cache.Set(domainListCacheKey, value); err != nil {
					logger.Error(fmt.Sprintf("[ %s ] cache domain list failed: %v", domainListCacheKey, err))
				}
				mu.Unlock()

				recordListCacheKey := public.RecordList + "_" + cloudProvider + "_" + cloudName
				records, err := dnsProvider.ListRecords()
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] list records failed: %v", recordListCacheKey, err))
					return
				}
				mu.Lock()
				value, err = json.Marshal(records)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] marshal record list failed: %v", recordListCacheKey, err))
				} else {
					if err := public.Cache.Set(recordListCacheKey, value); err != nil {
						logger.Error(fmt.Sprintf("[ %s ] cache record list failed: %v", recordListCacheKey, err))
					} else {
						logger.Info(fmt.Sprintf("[ %s ] successfully cached %d records", recordListCacheKey, len(records)))
					}
				}
				mu.Unlock()
			}(cloudProvider, cloudAccount["name"], cloudAccount)
		}
	}
	wg.Wait()
}

func loadingCert() {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for cloudProvider, accounts := range public.Config.CloudProviders {
		for _, cloudAccount := range accounts.Accounts {
			wg.Add(1)
			go func(cloudProvider, cloudName string, account map[string]string) {
				defer wg.Done()
				recordListCacheKey := public.RecordList + "_" + cloudProvider + "_" + cloudName
				var records []provider.Record
				rst2, err := public.Cache.Get(recordListCacheKey)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] get record list from cache failed: %v", recordListCacheKey, err))
					return // 缓存获取失败时直接返回
				}

				err = json.Unmarshal(rst2, &records)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] json.Unmarshal error: %v", recordListCacheKey, err))
					return // JSON解析失败时直接返回
				}

				if len(records) == 0 {
					logger.Info(fmt.Sprintf("[ %s ] no records found in cache, skipping cert collection", recordListCacheKey))
					return // 没有记录时直接返回
				}

				logger.Info(fmt.Sprintf("[ %s ] found %d records in cache, starting cert collection", recordListCacheKey, len(records)))
				var recordCertReq []provider.GetRecordCertReq
				for _, v := range getNewRecord(records) {
					recordCertReq = append(recordCertReq, provider.GetRecordCertReq{
						CloudProvider: v.CloudProvider,
						CloudName:     v.CloudName,
						DomainName:    v.DomainName,
						DomainType:    v.DomainType, // 新增：传递域名类型
						FullRecord:    v.FullRecord,
						RecordValue:   v.RecordValue,
						RecordID:      v.RecordID,
					})
				}
				recordCerts, err := GetMultipleCertInfo(recordCertReq)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] get record cert info failed: %v", recordListCacheKey, err))
					return
				}

				mu.Lock()
				recordCertInfoCacheKey := public.RecordCertInfo + "_" + cloudProvider + "_" + cloudName
				value, err := json.Marshal(recordCerts)
				if err != nil {
					logger.Error(fmt.Sprintf("[ %s ] marshal cert list failed: %v", recordCertInfoCacheKey, err))
				} else {
					if err := public.CertCache.Set(recordCertInfoCacheKey, value); err != nil {
						logger.Error(fmt.Sprintf("[ %s ] cache cert list failed: %v", recordCertInfoCacheKey, err))
					} else {
						logger.Info(fmt.Sprintf("[ %s ] successfully cached %d cert records", recordCertInfoCacheKey, len(recordCerts)))
					}
				}
				mu.Unlock()
			}(cloudProvider, cloudAccount["name"], cloudAccount)
		}
	}
	wg.Wait()
}

func loadingCustomRecordCert() {
	if len(public.Config.CustomRecords) == 0 {
		return
	}
	var records []provider.Record
	for _, v := range public.Config.CustomRecords {
		domainName, err := publicsuffix.Domain(v)
		if err != nil {
			logger.Error(fmt.Sprintf("[ custom ] get domain failed: %v", err))
		}
		records = append(records, provider.Record{
			CloudProvider: public.CustomRecords,
			CloudName:     public.CustomRecords,
			DomainName:    domainName,
			DomainType:    "public", // 自定义记录默认为公网类型
			FullRecord:    v,
			RecordValue:   v,
			RecordID:      public.GetID(),
			RecordType:    "CNAME", // 默认指定为CNAME记录,这两条记录为了通过检测
			RecordStatus:  "enable",
		})
	}
	var recordCertReq []provider.GetRecordCertReq
	for _, v := range getNewRecord(records) {
		recordCertReq = append(recordCertReq, provider.GetRecordCertReq{
			CloudProvider: v.CloudProvider,
			CloudName:     v.CloudName,
			DomainName:    v.DomainName,
			DomainType:    v.DomainType, // 新增：传递域名类型
			FullRecord:    v.FullRecord,
			RecordValue:   v.RecordValue,
			RecordID:      v.RecordID,
		})
	}
	recordCerts, err := GetMultipleCertInfo(recordCertReq)
	if err != nil {
		logger.Error(fmt.Sprintf("[ custom ] get record cert info failed: %v", err))
		return
	}
	recordCertInfoCacheKey := public.RecordCertInfo + "_" + public.CustomRecords
	value, err := json.Marshal(recordCerts)
	if err != nil {
		logger.Error(fmt.Sprintf("[ %s ] marshal domain list failed: %v", recordCertInfoCacheKey, err))
	}
	if err := public.CertCache.Set(recordCertInfoCacheKey, value); err != nil {
		logger.Error(fmt.Sprintf("[ %s ] cache domain list failed: %v", recordCertInfoCacheKey, err))
	}
}
