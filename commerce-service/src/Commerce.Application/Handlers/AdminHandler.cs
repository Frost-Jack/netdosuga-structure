using Commerce.Application.Shop;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class AdminHandler(IApplicationRepository applications) : IRequestHandler
{
    public bool CanHandle(string operation) => operation == "admin.vendors";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        var vendors = await applications.ListVendorsAsync(cancellationToken);
        return ShopOutcome.Ok(new Dictionary<string, object?>
        {
            ["vendors"] = vendors.Select(x => new Dictionary<string, object?>
            {
                ["id"] = x.Id,
                ["organization"] = x.Organization,
                ["applications"] = x.Applications,
                ["offers"] = x.Offers
            }).ToList()
        });
    }
}
