INSERT INTO playbooks (id, name, description, builtin, steps) VALUES
('00000000-0000-4000-8000-000000000003', 'Security baseline (CrowdSec)',
 'Firewall on (SSH kept open), CrowdSec reporting to the central LAPI with the nftables bouncer (bans shared by all servers), and all package updates.', true,
 '[{"op":"packages.upgrade","params":{"dist":"false","autoremove":"true"}},
   {"op":"firewall.install","params":{}},
   {"op":"firewall.enable","params":{}},
   {"op":"crowdsec.install","params":{}}]')
ON CONFLICT (id) DO NOTHING;
