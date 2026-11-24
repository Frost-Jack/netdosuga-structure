using System.Text.RegularExpressions;
using Commerce.Application.Shop;
using Commerce.Domain;

namespace Commerce.Application.Validation;

public static partial class ApplicationFieldsValidator
{
    public static void Validate(HandlerContext context)
    {
        RequireText(context, "organization");
        RequireText(context, "title");
        RequireText(context, "city");
        RequireText(context, "category");
        RequireText(context, "address");
        RequireText(context, "contact_person");
        RequireDigits(context, "inn", 10, 12);
        RequireDigits(context, "ogrn", 13, 15);
        RequireDigits(context, "bik", 9, 9);
        RequireDigits(context, "account", 20, 20);
    }

    private static void RequireText(HandlerContext context, string key)
    {
        var value = context.Text(key);
        if (value.Length is 0 or > 300)
        {
            throw new DomainException(DomainError.Bad("REQUIRED_FIELDS"));
        }
    }

    private static void RequireDigits(HandlerContext context, string key, int min, int max)
    {
        var value = context.Text(key);
        if (!Digits().IsMatch(value) || value.Length < min || value.Length > max)
        {
            throw new DomainException(DomainError.Bad("INVALID_" + key.ToUpperInvariant()));
        }
    }

    [GeneratedRegex(@"^\d+$")]
    private static partial Regex Digits();
}
