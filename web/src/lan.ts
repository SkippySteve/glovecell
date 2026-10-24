// LAN host grant helpers. The controller is the authority; these only catch
// typing mistakes before a request.
import type {LANHost} from './api';

export const MAX_LAN_PORTS = 16;
const IPV4 = /^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])$/;

export const isIPv4 = (value: string) => IPV4.test(value);

// "22, 8080" -> [22, 8080]. Ranges are refused: every port is listed explicitly.
export function parsePorts(text: string): number[] | string {
  const fields = text.split(/[\s,]+/).filter(Boolean);
  if (!fields.length) return 'List at least one TCP port.';
  if (fields.length > MAX_LAN_PORTS) return `List at most ${MAX_LAN_PORTS} ports.`;
  const ports: number[] = [];
  for (const field of fields) {
    if (!/^[0-9]{1,5}$/.test(field)) return field.includes('-') || field.includes(':') ? 'List each port; ranges are not accepted.' : `${field} is not a port number.`;
    const port = Number(field);
    if (port < 1 || port > 65535) return `${field} is outside 1–65535.`;
    if (ports.includes(port)) return `${port} is listed twice.`;
    ports.push(port);
  }
  return ports.sort((a, b) => a - b);
}

export const portsLabel = (h: LANHost) => 'TCP ' + h.ports.join(', ');
