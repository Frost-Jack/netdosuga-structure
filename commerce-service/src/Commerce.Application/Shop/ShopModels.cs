using System.Text.Json;
using Commerce.Domain.Ports;

namespace Commerce.Application.Shop;

public sealed record ShopCommand(string Operation, string Id, string BodyJson, string IdempotencyKey);

public sealed record ShopOutcome(int Status, IReadOnlyDictionary<string, object?> Body)
{
    public static ShopOutcome Ok(IReadOnlyDictionary<string, object?> body) => new(200, body);

    public static ShopOutcome Created(IReadOnlyDictionary<string, object?> body) => new(201, body);

    public static ShopOutcome Error(int status, string code) =>
        new(status, new Dictionary<string, object?> { ["code"] = code, ["message"] = code });
}

public sealed class HandlerContext
{
    private readonly JsonElement _body;

    public HandlerContext(ShopCommand command, ActorContext actor, JsonElement body)
    {
        Command = command;
        Actor = actor;
        _body = body;
    }

    public ShopCommand Command { get; }
    public ActorContext Actor { get; }
    public string Id => Command.Id;
    public string IdempotencyKey => Command.IdempotencyKey;

    public string Text(string key)
    {
        if (_body.ValueKind != JsonValueKind.Object || !_body.TryGetProperty(key, out var value))
        {
            return string.Empty;
        }

        return value.ValueKind == JsonValueKind.String ? (value.GetString() ?? string.Empty).Trim() : string.Empty;
    }

    public long Number(string key)
    {
        if (_body.ValueKind != JsonValueKind.Object || !_body.TryGetProperty(key, out var value))
        {
            return -1;
        }

        return value.ValueKind == JsonValueKind.Number && value.TryGetInt64(out var number) ? number : -1;
    }
}

public interface IRequestHandler
{
    bool CanHandle(string operation);

    Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken);
}
