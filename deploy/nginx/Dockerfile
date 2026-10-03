FROM nginx:1.28-alpine
COPY internal/cli/assets/nginx.conf.template /etc/nginx/templates/nginx.conf.template
COPY --chmod=755 deploy/nginx/15-hex-domain.envsh /docker-entrypoint.d/15-hex-domain.envsh
ENV NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx
ENV HEX_ANALYTICS_ENABLED=0
ENV NGINX_ENVSUBST_FILTER=^HEX_(SITE_DOMAIN_PATTERN|ANALYTICS_ENABLED)$
EXPOSE 8080
