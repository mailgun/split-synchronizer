job "split-synchronizer.proxy" {
  type = "service"

  vault {
    policies = ["default", "mg-services-reader"]
    change_mode = "signal"
    change_signal = "SIGHUP"
  }

  group "app" {
    count = 1

    update {
      healthy_deadline = "2m"
      max_parallel     = 1
      canary           = 1
      auto_revert      = true
      auto_promote     = true
    }

    restart {
      attempts = 10
      interval = "2m30s"
      delay    = "5s"
      mode     = "delay"
    }

    network {
      port "http" {
        to = "3000"
      }
      port "admin" {
        to = "3010"
      }
    }

    task "proxy" {
      driver = "docker"

      kill_timeout = "30s"
      kill_signal = "SIGTERM"

      shutdown_delay = "10s"

      config {
        image = "ghcr.io/mailgun/split-synchronizer:{@tag}"
        command = "split-proxy -config=/etc/splitio.config.json"
        force_pull = true

        ports = ["http", "admin"]

        volumes = [
          "{common[shared_tls_dir]}:{common[shared_tls_dir]}:ro",
          "{common[shared_tls_dir]}:/etc/mailgun/ssl:ro",
          "/var/mailgun:/var/mailgun",
          "secrets:/etc/secrets",
        ]
      }

            template {
        destination = "/etc/splitio.config.json"
        data = <<EOF
{
    "apikey":  "{{with secret "mg-services/informant"}}{{index .Data.data.split_proxy_server_key}}{{end}}",
    "server": {
        "apikeys":  "{{with secret "mg-services/informant"}}{{index .Data.data.split_proxy_client_key}}{{end}}"
    }
}
EOF
      }

      resources {
        cpu = 128
        memory = 128
      }

      service {
        name = "informant-split-proxy"
        port = "http"

        canary_tags = [
          "traefik.enable=false",
        ]

        tags = [
          "traefik.enable=true",

          # HTTP Router
          "traefik.http.routers.informant-split-proxy-http.entrypoints=http",
          "traefik.http.routers.informant-split-proxy-http.rule=Host(`api.${meta.base_domain}`) && PathPrefix(`/v1/inf/split`)",
          "traefik.http.routers.informant-split-proxy-http.service=vulcand-public",
          "traefik.http.routers.informant-split-proxy-http.middlewares=public-api@file",

          # HTTPS Router
          "traefik.http.routers.informant-split-proxy-https.entrypoints=https",
          "traefik.http.routers.informant-split-proxy-https.tls=true",
          "traefik.http.routers.informant-split-proxy-https.rule=Host(`api.${meta.base_domain}`) && PathPrefix(`/v1/inf/split`)",
          "traefik.http.routers.informant-split-proxy-https.service=informant-split-proxy",
          "traefik.http.routers.informant-split-proxy-https.middlewares=public-api@file",
        ]

        check {
          name     = "split proxy health"
          type     = "http"
          protocol = "http"
          interval = "10s"
          timeout  = "2s"
          path     = "/info/ping"
          port     = "admin"
          method   = "GET"
        }
      }
    }

  }
}
