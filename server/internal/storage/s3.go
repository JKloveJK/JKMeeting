package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Client struct {
	client *minio.Client
	bucket string
}

func NewS3Client(endpoint, accessKey, secretKey, bucket string) (*S3Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("解析 S3 endpoint 失败：%w", err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("S3 endpoint 缺少主机地址")
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: parsed.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("创建 S3 客户端失败：%w", err)
	}
	return &S3Client{client: client, bucket: bucket}, nil
}

func (client *S3Client) EnsureBucket(ctx context.Context) error {
	exists, err := client.client.BucketExists(ctx, client.bucket)
	if err != nil {
		return fmt.Errorf("检查 S3 bucket 失败：%w", err)
	}
	if exists {
		return nil
	}
	if err := client.client.MakeBucket(ctx, client.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("创建 S3 bucket 失败：%w", err)
	}
	return nil
}

func (client *S3Client) PresignedPut(ctx context.Context, objectKey, contentType string, expiry time.Duration) (string, error) {
	requestParams := make(url.Values)
	if strings.TrimSpace(contentType) != "" {
		requestParams.Set("Content-Type", contentType)
	}
	url, err := client.client.PresignedPutObject(ctx, client.bucket, objectKey, expiry)
	if err != nil {
		return "", fmt.Errorf("生成预签名 PUT URL 失败：%w", err)
	}
	_ = requestParams
	return url.String(), nil
}

func (client *S3Client) PresignedGet(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	url, err := client.client.PresignedGetObject(ctx, client.bucket, objectKey, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("生成预签名 GET URL 失败：%w", err)
	}
	return url.String(), nil
}

func (client *S3Client) DownloadToTemp(ctx context.Context, objectKey string, maxBytes int64) (string, error) {
	object, err := client.client.GetObject(ctx, client.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("下载 S3 对象失败：%w", err)
	}
	defer object.Close()

	file, err := os.CreateTemp("", "jkmeeting-transcribe-*.audio")
	if err != nil {
		return "", fmt.Errorf("创建临时音频文件失败：%w", err)
	}
	path := file.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	copied, copyErr := io.Copy(file, io.LimitReader(object, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("写入临时音频文件失败：%w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	if copied > maxBytes {
		_ = os.Remove(path)
		return "", fmt.Errorf("音频对象超过大小上限")
	}
	return path, nil
}

func (client *S3Client) UploadFile(ctx context.Context, objectKey, path, contentType string) error {
	if _, err := client.client.FPutObject(ctx, client.bucket, objectKey, path, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("上传 S3 对象失败：%w", err)
	}
	return nil
}

// ReadObject 读取小对象（如 HLS playlist），超过 maxBytes 视为错误。
func (client *S3Client) ReadObject(ctx context.Context, objectKey string, maxBytes int64) ([]byte, error) {
	object, err := client.client.GetObject(ctx, client.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("读取 S3 对象失败：%w", err)
	}
	defer object.Close()
	body, err := io.ReadAll(io.LimitReader(object, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 S3 对象失败：%w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("S3 对象超过大小上限")
	}
	return body, nil
}

func (client *S3Client) Exists(ctx context.Context, objectKey string) (bool, error) {
	_, err := client.client.StatObject(ctx, client.bucket, objectKey, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	response := minio.ToErrorResponse(err)
	if response.Code == "NoSuchKey" || response.Code == "NoSuchObject" || response.StatusCode == 404 {
		return false, nil
	}
	return false, fmt.Errorf("检查 S3 对象失败：%w", err)
}
