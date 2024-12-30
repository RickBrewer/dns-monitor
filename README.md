# DNS Monitor

A DNS monitoring service that checks DNS records against expected values and provides a web interface for status monitoring.

I wanted Uptime Kuma to check results of DNS queried records, but it doesn't support it. I created this project to monitor DNS records.
This can be done by using an HTTP/HTTPS keyword monitor against the web page this app builds.

Containers for this app are at https://hub.docker.com/r/rickbrewer/dns-monitor

## Features
- Monitors multiple DNS record types (A, CNAME, NS, TXT, MX)
- Configurable check intervals per domain
- Primary and secondary DNS server support
- Customizable web interface port
- 30-day logging history with automatic cleanup
- Real-time status monitoring via web interface
- Status tracking for each DNS check
- Concurrent monitoring for multiple domains
- Automatic log directory creation


## Configuration
Create a `config.yaml` file in the same directory as the executable. Here's a complete configuration example:

```yaml
global:
  dns_server: "8.8.8.8"                # Primary DNS server
  secondary_dns_server: "8.8.4.4"      # Optional secondary DNS server
  default_interval: 5m                 # Default check interval if not specified per check
  log_dir: "logs"                      # Directory for storing check history
  port: "8080"                         # Web interface port (optional, defaults to 8080)

checks:
  - domain: example.com
    type: NS                          # Record type (A, CNAME, NS, TXT, MX)
    expected: ns1.example.com         # Expected value in the DNS record
    interval: 1h                      # Check interval (overrides default_interval)

  - domain: example.org
    type: A
    expected: 93.184.216.34
    interval: 5m

  - domain: example.net
    type: MX
    expected: mail.example.net
    # Uses default_interval since interval is not specified