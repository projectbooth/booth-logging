{
  "realm": "booth",
  "enabled": true,
  "sslRequired": "none",
  "groups": [
    {
      "name": "workspaces",
      "subGroups": [
        {
          "name": "acme-analytics",
          "subGroups": [{"name": "owner"}, {"name": "editor"}, {"name": "viewer"}]
        }
      ]
    },
    {
      "name": "platform",
      "subGroups": [
        {"name": "operator", "subGroups": [{"name": "readonly"}]},
        {"name": "operators"}
      ]
    }
  ],
  "clients": [
    {
      "clientId": "booth-design",
      "enabled": true,
      "publicClient": true,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": true,
      "redirectUris": ["*"],
      "webOrigins": ["*"],
      "attributes": {"pkce.code.challenge.method": "S256"},
      "protocolMappers": [
        {
          "name": "groups",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-group-membership-mapper",
          "config": {
            "claim.name": "groups",
            "full.path": "true",
            "id.token.claim": "true",
            "access.token.claim": "true",
            "userinfo.token.claim": "true"
          }
        },
        {
          "name": "booth-design-audience",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-audience-mapper",
          "config": {
            "included.client.audience": "booth-design",
            "id.token.claim": "false",
            "access.token.claim": "true"
          }
        }
      ]
    }
  ],
  "users": [
    {
      "username": "operator-user",
      "enabled": true, "emailVerified": true, "email": "operator-user@example.test",
      "firstName": "Operator", "lastName": "User",
      "credentials": [{"type": "password", "value": "__TEST_PASSWORD__", "temporary": false}],
      "groups": ["/workspaces/acme-analytics/owner", "/platform/operator"]
    },
    {
      "username": "owner-user",
      "enabled": true, "emailVerified": true, "email": "owner-user@example.test",
      "firstName": "Owner", "lastName": "User",
      "credentials": [{"type": "password", "value": "__TEST_PASSWORD__", "temporary": false}],
      "groups": ["/workspaces/acme-analytics/owner"]
    },
    {
      "username": "nearmiss-plural-user",
      "enabled": true, "emailVerified": true, "email": "nearmiss-plural-user@example.test",
      "firstName": "Nearmiss", "lastName": "Plural",
      "credentials": [{"type": "password", "value": "__TEST_PASSWORD__", "temporary": false}],
      "groups": ["/workspaces/acme-analytics/owner", "/platform/operators"]
    },
    {
      "username": "nearmiss-child-user",
      "enabled": true, "emailVerified": true, "email": "nearmiss-child-user@example.test",
      "firstName": "Nearmiss", "lastName": "Child",
      "credentials": [{"type": "password", "value": "__TEST_PASSWORD__", "temporary": false}],
      "groups": ["/workspaces/acme-analytics/owner", "/platform/operator/readonly"]
    }
  ]
}
