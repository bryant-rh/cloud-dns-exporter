# 阿里云 STS 认证和公网/内网域名采集增强

## 功能概述

本次增强为阿里云 DNS 提供商添加了以下功能：

1. **STS (Security Token Service) 认证支持** ✅ 已完成
2. **公网和内网域名同时采集** ✅ 已完成
3. **域名类型标签区分** (public/private) ✅ 已完成

## 实现状态

### ✅ 已完成功能

#### 1. STS 认证支持
- **自动凭证刷新**：STS 临时凭证会在过期前 5 分钟自动刷新
- **向下兼容**：如果不配置 `roleArn`，系统会使用原始的 AccessKey/SecretKey 认证
- **并发安全**：使用读写锁保护凭证刷新过程，支持高并发场景
- **错误处理**：STS 认证失败时会回退到原始认证方式

#### 2. 公网和内网域名采集
- **公网域名**：通过 DNS API 采集所有公网解析域名
- **内网域名**：通过 PrivateZone API 采集所有内网解析域名
- **域名类型标识**：
  - 公网域名：`domain_type="public"`，DomainID 格式：`public_<domain_name>`
  - 内网域名：`domain_type="private"`，DomainID 格式：`private_<zone_id>`

#### 3. DNS 记录采集
- 自动为每个域名（公网+内网）采集所有 DNS 解析记录
- 支持所有记录类型：A、AAAA、CNAME、MX、TXT、NS 等
- 保留原有的记录状态、TTL、权重等信息

#### 4. Prometheus 指标增强
- 所有 DNS 相关指标都新增了 `domain_type` 标签
- 支持按域名类型进行监控和告警

## 配置方式

### 基本配置（向下兼容）
```yaml
cloud_providers:
  aliyun:
    accounts:
      - name: aliyun-basic
        secretId: "your_access_key_id"
        secretKey: "your_access_key_secret"
```

### STS 认证配置（推荐）
```yaml
cloud_providers:
  aliyun:
    accounts:
      - name: aliyun-sts
        secretId: "your_access_key_id"
        secretKey: "your_access_key_secret"
        roleArn: "acs:ram::123456789012:role/AliyunDNSExporterRole"  # 新增 STS ARN
```

## 权限配置

### STS 角色权限要求
```json
{
    "Version": "1",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "alidns:DescribeDomains",
                "alidns:DescribeDomainRecords",
                "pvtz:DescribeZones",
                "pvtz:DescribeZoneRecords"
            ],
            "Resource": "*"
        }
    ]
}
```

### RAM 用户权限要求
```json
{
    "Version": "1",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "sts:AssumeRole"
            ],
            "Resource": "acs:ram::123456789012:role/AliyunDNSExporterRole"
        }
    ]
}
```

## Prometheus 指标示例

### 域名列表指标
```
# 公网域名
domain_list{cloud_provider="aliyun",cloud_name="prod",domain_type="public",domain_name="example.com"} 0

# 内网域名
domain_list{cloud_provider="aliyun",cloud_name="prod",domain_type="private",domain_name="internal.local"} 0
```

### DNS 记录指标
```
# 公网 DNS 记录
record_list{cloud_provider="aliyun",cloud_name="prod",domain_type="public",domain_name="api.example.com"} 1

# 内网 DNS 记录
record_list{cloud_provider="aliyun",cloud_name="prod",domain_type="private",domain_name="internal.local"} 1
```

### 证书信息指标
```
# 公网域名证书
record_cert_info{cloud_provider="aliyun",cloud_name="prod",domain_type="public",domain_name="api.example.com"} 30

# 内网域名证书
record_cert_info{cloud_provider="aliyun",cloud_name="prod",domain_type="private",domain_name="internal.local"} 30
```

## 监控查询示例

### PromQL 查询
```promql
# 查询所有公网域名
domain_list{domain_type="public"}

# 查询内网 DNS 记录数量
count(record_list{domain_type="private"})

# 查询即将过期的公网域名证书（30天内）
record_cert_info{domain_type="public"} < 30

# 按域名类型统计域名数量
count by (domain_type) (domain_list)

# 查询特定云账号的内网域名
domain_list{cloud_name="prod",domain_type="private"}
```

### 告警规则示例
```yaml
groups:
  - name: dns_alerts
    rules:
      - alert: PublicDomainCertExpiring
        expr: record_cert_info{domain_type="public"} < 7
        labels:
          severity: warning
        annotations:
          summary: "公网域名证书即将过期"
          description: "域名 {{ $labels.domain_name }} 的证书将在 {{ $value }} 天后过期"
          
      - alert: PrivateDNSRecordDown
        expr: up{job="dns-exporter"} == 0 and domain_type="private"
        labels:
          severity: critical
        annotations:
          summary: "内网 DNS 记录采集失败"
          description: "内网 DNS 记录采集服务异常"

      - alert: STSAuthenticationFailed
        expr: increase(sts_auth_failures_total[5m]) > 0
        labels:
          severity: warning
        annotations:
          summary: "STS 认证失败"
          description: "阿里云 STS 认证失败，请检查角色配置"
```

## 技术实现细节

### STS 认证流程
1. 检查是否配置了 `roleArn`
2. 使用原始凭证调用 STS AssumeRole API
3. 获取临时凭证（AccessKeyId、AccessKeySecret、SecurityToken）
4. 使用临时凭证创建 DNS 和 PrivateZone 客户端
5. 在凭证过期前 5 分钟自动刷新

### 域名采集流程
1. **公网域名采集**：调用 `alidns.DescribeDomains` API
2. **内网域名采集**：调用 `pvtz.DescribeZones` API  
3. **记录采集**：为每个域名调用对应的记录查询 API
4. **数据转换**：统一转换为通用的 Domain/Record 结构
5. **指标暴露**：通过 Prometheus 指标暴露，包含 domain_type 标签

### 错误处理
- STS 认证失败时自动回退到原始认证
- 单个域名采集失败不影响其他域名
- 公网/内网采集相互独立，一个失败不影响另一个
- 详细的错误日志记录，便于问题排查

## 兼容性说明

- **向下兼容**：现有配置无需修改，不配置 `roleArn` 时使用原有认证方式
- **指标兼容**：新增的 `domain_type` 标签不影响现有查询，只是增加了更精确的过滤能力
- **API 兼容**：使用阿里云官方 SDK，API 调用方式标准且稳定

## 性能优化

- **并发采集**：公网和内网域名采集并行执行
- **凭证缓存**：STS 临时凭证缓存复用，减少 API 调用
- **分页查询**：大量域名时使用分页查询，避免超时
- **错误隔离**：单个域名错误不影响整体采集进度

## 部署验证

### 1. 编译验证
```bash
go build -o cloud_dns_exporter .
```

### 2. 配置验证
```bash
# 检查配置文件格式
./cloud_dns_exporter --config config.yaml --dry-run
```

### 3. 功能验证
```bash
# 启动服务
./cloud_dns_exporter

# 检查指标
curl http://localhost:8080/metrics | grep domain_type
```

## 总结

✅ **已完成所有功能**：
- STS 认证支持（包含自动刷新和错误处理）
- 公网和内网域名同时采集
- 域名类型标签区分（public/private）
- Prometheus 指标增强
- 向下兼容性保证

该实现完全满足用户需求，提供了安全、高效、可监控的阿里云 DNS 域名采集解决方案。