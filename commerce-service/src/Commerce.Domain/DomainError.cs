namespace Commerce.Domain;

public sealed record DomainError(int Status, string Code)
{
    public static DomainError Bad(string code) => new(400, code);
    public static DomainError Forbidden(string code = "FORBIDDEN") => new(403, code);
    public static DomainError NotFound(string code = "NOT_FOUND") => new(404, code);
    public static DomainError Conflict(string code) => new(409, code);
    public static DomainError Unavailable(string code = "UNAVAILABLE") => new(503, code);
    public static DomainError Timeout(string code = "MOCK_TIMEOUT") => new(504, code);
}

public sealed class DomainException(DomainError error) : Exception(error.Code)
{
    public DomainError Error { get; } = error;
}
