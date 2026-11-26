# 生产 nginx 配置（宿主托管，不是镜像里的那份）

生产容器把这两份文件 **bind mount** 进容器，覆盖镜像自带的 `conf.d/default.conf`：

```
/opt/lanqin-email/nginx/site-80.conf  -> /etc/nginx/conf.d/default.conf
/opt/lanqin-email/nginx/ssl-443.conf  -> /etc/nginx/conf.d/ssl-443.conf
```

所以：

- **改 `deploy/all-in-one/nginx.conf` 不会影响生产**，它只服务「没有这两个挂载」的全新安装/测试 runtime。
- 要在生产改压缩、缓存、CSP 这类 nginx 行为，改的是宿主上这两个文件，然后
  `docker exec lanqin-email-lanqin-email-1 nginx -t && docker exec lanqin-email-lanqin-email-1 nginx -s reload`
  （reload 是平滑的，不用重启容器、不断连接）。
- `ssl-443.conf` 里 `add_header` 是逐 location 重复的：nginx 的 `add_header` 不继承，某个
  location 一旦自己写了一条，server 级的其余头就全丢。加缓存头时必须把 HSTS/CSP 等
  一起再写一遍。
- 镜像里 `/usr/share/nginx/html` 有构建期 `gzip -9` 生成的 `.gz`，宿主配置的
  `gzip_static on` 直接发它们；`.gz` 缺失时回落到动态 gzip。

这两份文件放仓库里只为可追溯/可重建；生产以宿主上的实际文件为准（改完记得回写）。
